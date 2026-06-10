package auth

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/yourorg/k8s-dashboard/config"
)

// OIDCHandler manages the Authorization Code flow against Keycloak.
// It plugs into the existing session layer via createToken — everything
// downstream (Middleware, RequireAdmin, /api/me, frontend role gating)
// is unchanged regardless of whether a session came from local credentials
// or from Keycloak. See docs/ARCHITECTURE.md §7.3.
type OIDCHandler struct {
	cfg        config.OIDCConfig
	httpClient *http.Client // custom client (TLS skip or default)
	verifier   *gooidc.IDTokenVerifier
	oauth2     oauth2.Config
	secret     string // dashboard session-signing secret
}

// NewOIDCHandler initialises the OIDC provider by fetching the discovery
// document from <issuer_url>/.well-known/openid-configuration.
// Returns nil, nil when OIDC is disabled — callers must check for nil.
func NewOIDCHandler(cfg config.OIDCConfig, sessionSecret string) (*OIDCHandler, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	if cfg.ClientSecret == "" {
		return nil, fmt.Errorf("OIDC_CLIENT_SECRET env var is not set (required when oidc.enabled = true)")
	}

	// Build the HTTP client used for the discovery fetch and token exchange.
	// tls_skip_verify is for environments where Keycloak uses an internal CA
	// that isn't in the OS trust store (e.g. a self-signed cert on a private
	// cluster). Never set this when Keycloak is fronted by a public CA.
	httpClient := &http.Client{Timeout: 10 * time.Second}
	if cfg.TLSSkipVerify {
		slog.Warn("oidc: TLS verification disabled — only use this with trusted internal CAs", "component", "auth")
		httpClient.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
		}
	}

	// oidc.ClientContext injects our custom HTTP client so both the discovery
	// fetch and subsequent token-exchange requests use it.
	ctx, cancel := context.WithTimeout(
		gooidc.ClientContext(context.Background(), httpClient),
		10*time.Second,
	)
	defer cancel()

	provider, err := gooidc.NewProvider(ctx, cfg.IssuerURL)
	if err != nil {
		return nil, fmt.Errorf("cannot reach issuer %s: %w", cfg.IssuerURL, err)
	}

	return &OIDCHandler{
		cfg:        cfg,
		httpClient: httpClient,
		verifier:   provider.Verifier(&gooidc.Config{ClientID: cfg.ClientID}),
		oauth2: oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			RedirectURL:  cfg.RedirectURL,
			Endpoint:     provider.Endpoint(),
			Scopes:       []string{gooidc.ScopeOpenID, "profile", "email"},
		},
		secret: sessionSecret,
	}, nil
}

// LoginHandler redirects the browser to the Keycloak authorization endpoint.
// Registered at GET /auth/login (public — in the auth.Middleware bypass list).
func (h *OIDCHandler) LoginHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		state := randomToken()
		nonce := randomToken()
		http.SetCookie(w, shortCookie("oidc_state", state))
		http.SetCookie(w, shortCookie("oidc_nonce", nonce))
		http.Redirect(w, r, h.oauth2.AuthCodeURL(state, gooidc.Nonce(nonce)), http.StatusFound)
	}
}

// CallbackHandler processes the redirect back from Keycloak:
//  1. verifies the state cookie (CSRF protection)
//  2. exchanges the authorization code for tokens
//  3. verifies the ID token signature, expiry, audience, and nonce
//  4. extracts the Keycloak realm roles and maps them to admin/viewer
//  5. mints the existing k8s_session cookie so the rest of the app is unchanged
//
// Registered at GET /auth/callback (public — in the auth.Middleware bypass list).
func (h *OIDCHandler) CallbackHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// ── 1. Verify state (anti-CSRF) ──────────────────────────────────────
		stateCookie, err := r.Cookie("oidc_state")
		if err != nil || r.URL.Query().Get("state") != stateCookie.Value {
			slog.Warn("oidc: state mismatch — possible CSRF attempt", "component", "auth")
			http.Redirect(w, r, "/login?error=1", http.StatusFound)
			return
		}
		nonceCookie, err := r.Cookie("oidc_nonce")
		if err != nil {
			http.Redirect(w, r, "/login?error=1", http.StatusFound)
			return
		}

		// ── 2. Exchange code for tokens ───────────────────────────────────────
		// Inject the custom HTTP client (same TLS config used at init time) so
		// the token-exchange request uses the same trust settings.
		ctx := gooidc.ClientContext(r.Context(), h.httpClient)
		oauth2Token, err := h.oauth2.Exchange(ctx, r.URL.Query().Get("code"))
		if err != nil {
			slog.Error("oidc: code exchange failed", "component", "auth", "error", err)
			http.Redirect(w, r, "/login?error=1", http.StatusFound)
			return
		}
		rawIDToken, ok := oauth2Token.Extra("id_token").(string)
		if !ok {
			slog.Error("oidc: no id_token in token response", "component", "auth")
			http.Redirect(w, r, "/login?error=1", http.StatusFound)
			return
		}

		// ── 3. Verify ID token ────────────────────────────────────────────────
		idToken, err := h.verifier.Verify(ctx, rawIDToken)
		if err != nil {
			slog.Error("oidc: id_token verification failed", "component", "auth", "error", err)
			http.Redirect(w, r, "/login?error=1", http.StatusFound)
			return
		}

		// ── 4. Extract claims ─────────────────────────────────────────────────
		var claims struct {
			Nonce             string `json:"nonce"`
			PreferredUsername string `json:"preferred_username"`
			RealmAccess       struct {
				Roles []string `json:"roles"`
			} `json:"realm_access"`
		}
		if err := idToken.Claims(&claims); err != nil {
			slog.Error("oidc: cannot parse id_token claims", "component", "auth", "error", err)
			http.Redirect(w, r, "/login?error=1", http.StatusFound)
			return
		}
		if claims.Nonce != nonceCookie.Value {
			slog.Warn("oidc: nonce mismatch", "component", "auth")
			http.Redirect(w, r, "/login?error=1", http.StatusFound)
			return
		}

		// ── 5. Map Keycloak realm role → dashboard role ───────────────────────
		// Keycloak only includes realm_access in the ID token when the client
		// scope mapper has "Add to ID token" enabled. As a robust fallback, we
		// also decode the access token payload (trusted: received directly from
		// Keycloak's token endpoint over TLS) to pick up realm roles even without
		// that mapper configured.
		realmRoles := claims.RealmAccess.Roles
		if len(realmRoles) == 0 {
			realmRoles = realmRolesFromAccessToken(oauth2Token.AccessToken)
		}
		slog.Debug("oidc: resolved realm roles", "component", "auth",
			"username", claims.PreferredUsername, "roles", realmRoles)

		role := ""
		switch {
		case slices.Contains(realmRoles, h.cfg.AdminRole):
			role = RoleAdmin
		case slices.Contains(realmRoles, h.cfg.ViewerRole):
			role = RoleViewer
		}
		if role == "" {
			slog.Warn("oidc: user authenticated but has no dashboard role assigned",
				"component", "auth",
				"username", claims.PreferredUsername,
				"realm_roles", claims.RealmAccess.Roles,
				"expected_admin", h.cfg.AdminRole,
				"expected_viewer", h.cfg.ViewerRole,
			)
			http.Redirect(w, r, "/login?error=access_denied", http.StatusFound)
			return
		}

		// ── 6. Mint session cookie (unchanged from local-auth path) ───────────
		slog.Info("oidc login succeeded", "component", "auth", "event", "oidc_login_success",
			"username", claims.PreferredUsername, "role", role)
		loginSuccessTotal.Add(1)

		http.SetCookie(w, &http.Cookie{
			Name:     cookieName,
			Value:    createToken(claims.PreferredUsername, role, h.secret),
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
			MaxAge:   int(sessionDuration.Seconds()),
		})
		http.Redirect(w, r, "/", http.StatusFound)
	}
}

// shortCookie returns a short-lived HttpOnly cookie for OIDC state/nonce.
// Path is "/" so the browser sends it back on the callback regardless of
// the exact path — simpler and equally safe since MaxAge is 5 minutes.
func shortCookie(name, value string) *http.Cookie {
	return &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   300, // 5 minutes — just long enough for the round-trip
	}
}

// randomToken generates a 16-byte cryptographically random hex string for use
// as an OIDC state or nonce value. Panics if the OS RNG is unavailable (that
// is a fatal system condition; no useful fallback exists).
func randomToken() string {
	return GenerateSecret()[:32] // GenerateSecret returns 64 hex chars; 32 is plenty
}

// realmRolesFromAccessToken decodes the Keycloak access token (a JWT) and
// returns the roles from the realm_access claim. No signature verification is
// performed — the token was received directly from Keycloak's token endpoint
// over TLS, so its origin is already trusted. This is only used as a fallback
// when the ID token's realm_access claim is absent (i.e. the client scope
// mapper "Add to ID token" is not enabled in Keycloak).
func realmRolesFromAccessToken(accessToken string) []string {
	parts := strings.SplitN(accessToken, ".", 3)
	if len(parts) != 3 {
		return nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil
	}
	var claims struct {
		RealmAccess struct {
			Roles []string `json:"roles"`
		} `json:"realm_access"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil
	}
	return claims.RealmAccess.Roles
}

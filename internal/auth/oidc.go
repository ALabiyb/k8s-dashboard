package auth

// ---------------------------------------------------------------------------
// Author: Labiyb M. Said — DevSecOps Engineer
// Contact: abdulmunimsaid82@gmail.com
// ---------------------------------------------------------------------------

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/ALabiyb/k8s-dashboard/config"
)

// OIDCHandler manages the Authorization Code flow against Keycloak.
// On a successful callback it creates a Session in the store containing the
// user's identity and the OIDC access + refresh tokens; the returned cookie
// holds only the opaque session ID. Middleware (auth.go) is unchanged.
type OIDCHandler struct {
	cfg        config.OIDCConfig
	httpClient *http.Client // custom client (TLS skip or default)
	verifier   *gooidc.IDTokenVerifier
	oauth2     oauth2.Config
	store      *SessionStore // owns session state and OIDC tokens
}

// NewOIDCHandler initialises the OIDC provider by fetching the discovery
// document from <issuer_url>/.well-known/openid-configuration.
// Returns nil, nil when OIDC is disabled — callers must check for nil.
func NewOIDCHandler(cfg config.OIDCConfig, store *SessionStore) (*OIDCHandler, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	if cfg.ClientSecret == "" {
		return nil, fmt.Errorf("OIDC_CLIENT_SECRET env var is not set (required when oidc.enabled = true)")
	}
	if store == nil {
		return nil, fmt.Errorf("OIDC handler requires a non-nil SessionStore")
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
		store: store,
	}, nil
}

// Errors returned by ValidAccessToken.
var (
	// ErrNoOIDCSession is returned when the session id is unknown or the
	// session has no OIDC tokens (e.g. a local username/password login).
	// K8s handlers should treat it as "not authenticated for K8s" — 401 or
	// re-redirect to /auth/login.
	ErrNoOIDCSession = fmt.Errorf("oidc: no OIDC session for id")

	// ErrRefreshFailed is returned when Keycloak refuses the refresh token
	// (revoked, expired, or the refresh session on Keycloak side is over).
	// The user must sign in again via the browser.
	ErrRefreshFailed = fmt.Errorf("oidc: refresh token exchange failed")
)

// ValidAccessToken returns a non-expired access token for the session,
// refreshing it via Keycloak if the stored one is stale.
//
// If a refresh happens, the new access + refresh tokens are persisted back
// to the session store so the next call can reuse them without hitting
// Keycloak again.
//
// Handlers should call this immediately before every Kubernetes API request
// (K8s access tokens typically live ~5 minutes; long-running dashboard
// sessions absolutely will outlive them).
func (h *OIDCHandler) ValidAccessToken(ctx context.Context, sessionID string) (string, error) {
	sess := h.store.Get(sessionID)
	if sess == nil || sess.AccessToken == "" || sess.RefreshToken == "" {
		return "", ErrNoOIDCSession
	}

	// Build an oauth2.Token from what we have and hand it to a TokenSource.
	// The TokenSource compares Expiry against time.Now() (with a small skew)
	// and refreshes automatically if needed. Otherwise it returns the same
	// token back — cheap.
	tok := &oauth2.Token{
		AccessToken:  sess.AccessToken,
		RefreshToken: sess.RefreshToken,
		Expiry:       sess.TokenExpiry,
	}
	// Inject our HTTP client (may skip TLS verify on the internal CA).
	oauthCtx := gooidc.ClientContext(ctx, h.httpClient)
	ts := h.oauth2.TokenSource(oauthCtx, tok)
	newTok, err := ts.Token()
	if err != nil {
		slog.Warn("oidc: refresh failed", "component", "auth",
			"username", sess.Username, "error", err)
		return "", fmt.Errorf("%w: %v", ErrRefreshFailed, err)
	}

	// Only touch the store if something actually changed. Reduces mutex
	// contention on the common "still valid, no refresh needed" path.
	if newTok.AccessToken != sess.AccessToken || newTok.RefreshToken != sess.RefreshToken {
		sess.AccessToken = newTok.AccessToken
		if newTok.RefreshToken != "" {
			// Keycloak may or may not rotate refresh tokens; only overwrite
			// when a new one is supplied — never with an empty string.
			sess.RefreshToken = newTok.RefreshToken
		}
		sess.TokenExpiry = newTok.Expiry
		h.store.Update(sessionID, sess)
		slog.Debug("oidc: access token refreshed", "component", "auth",
			"username", sess.Username, "new_expiry", newTok.Expiry)
	}
	return newTok.AccessToken, nil
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

// CallbackHandler processes the redirect back from Keycloak.
// Registered at GET /auth/callback (public — in the auth.Middleware bypass list).
func (h *OIDCHandler) CallbackHandler() http.HandlerFunc {
	return h.handleOIDCCallback
}

// handleOIDCCallback is the named implementation behind CallbackHandler.
// Extracting it from the closure removes the nesting penalty on cognitive
// complexity and makes the steps easier to test in isolation.
//
//  1. verifies the state cookie (CSRF protection)
//  2. exchanges the authorization code for tokens
//  3. verifies the ID token signature, expiry, audience, and nonce
//  4. extracts the Keycloak realm roles and maps them to admin/viewer
//  5. mints the k8s_session cookie so the rest of the app is unchanged
func (h *OIDCHandler) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	// ── 1. Verify state (anti-CSRF) ──────────────────────────────────────
	stateCookie, err := r.Cookie("oidc_state")
	if err != nil || r.URL.Query().Get("state") != stateCookie.Value {
		slog.Warn("oidc: state mismatch — possible CSRF attempt", "component", "auth")
		http.Redirect(w, r, loginErrPath, http.StatusFound)
		return
	}
	nonceCookie, err := r.Cookie("oidc_nonce")
	if err != nil {
		http.Redirect(w, r, loginErrPath, http.StatusFound)
		return
	}

	// ── 2. Exchange code for tokens ───────────────────────────────────────
	// Inject the custom HTTP client (same TLS config used at init time) so
	// the token-exchange request uses the same trust settings.
	ctx := gooidc.ClientContext(r.Context(), h.httpClient)
	oauth2Token, err := h.oauth2.Exchange(ctx, r.URL.Query().Get("code"))
	if err != nil {
		slog.Error("oidc: code exchange failed", "component", "auth", "error", err)
		http.Redirect(w, r, loginErrPath, http.StatusFound)
		return
	}
	rawIDToken, ok := oauth2Token.Extra("id_token").(string)
	if !ok {
		slog.Error("oidc: no id_token in token response", "component", "auth")
		http.Redirect(w, r, loginErrPath, http.StatusFound)
		return
	}

	// ── 3. Verify ID token ────────────────────────────────────────────────
	idToken, err := h.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		slog.Error("oidc: id_token verification failed", "component", "auth", "error", err)
		http.Redirect(w, r, loginErrPath, http.StatusFound)
		return
	}

	// ── 4. Extract claims ─────────────────────────────────────────────────
	// We read `groups` (populated by the Keycloak Group Membership mapper
	// on the k8s-dashboard client, with "Full group path" OFF so entries
	// are plain names like "k8s-cluster-admins", not "/k8s-cluster-admins").
	var claims struct {
		Nonce             string   `json:"nonce"`
		PreferredUsername string   `json:"preferred_username"`
		Groups            []string `json:"groups"`
	}
	if err := idToken.Claims(&claims); err != nil {
		slog.Error("oidc: cannot parse id_token claims", "component", "auth", "error", err)
		http.Redirect(w, r, loginErrPath, http.StatusFound)
		return
	}
	if claims.Nonce != nonceCookie.Value {
		slog.Warn("oidc: nonce mismatch", "component", "auth")
		http.Redirect(w, r, loginErrPath, http.StatusFound)
		return
	}

	// ── 5. Map Keycloak groups → dashboard role ───────────────────────────
	// If groups aren't in the ID token (mapper not configured with "Add to
	// ID token"), fall back to decoding the access token — same trust
	// argument as the previous realm_roles fallback: the access token was
	// received directly from Keycloak's token endpoint over TLS.
	groups := claims.Groups
	if len(groups) == 0 {
		groups = groupsFromAccessToken(oauth2Token.AccessToken)
	}
	slog.Debug("oidc: resolved groups", "component", "auth",
		"username", claims.PreferredUsername, "groups", groups)

	role := roleFromGroups(groups, h.cfg.AdminGroup)
	if role == "" {
		slog.Warn("oidc: user authenticated but has no k8s-* group assigned",
			"component", "auth",
			"username", claims.PreferredUsername,
			"groups", groups,
			"expected_admin_group", h.cfg.AdminGroup,
		)
		http.Redirect(w, r, "/login?error=access_denied", http.StatusFound)
		return
	}

	// ── 6. Mint session (server-side; cookie holds only the opaque ID) ────
	// Store the OIDC access + refresh tokens so K8s-facing handlers can
	// forward the user's identity to the API server. The refresh token
	// stays in the store (never sent to the browser).
	slog.Info("oidc login succeeded", "component", "auth", "event", "oidc_login_success",
		"username", claims.PreferredUsername, "role", role, "groups", groups)
	loginSuccessTotal.Add(1)

	sessionID := h.store.Create(&Session{
		Username:     claims.PreferredUsername,
		Role:         role,
		Groups:       groups,
		AccessToken:  oauth2Token.AccessToken,
		RefreshToken: oauth2Token.RefreshToken,
		TokenExpiry:  oauth2Token.Expiry,
		ExpiresAt:    time.Now().Add(sessionDuration),
	})
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    sessionID,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionDuration.Seconds()),
	})
	http.Redirect(w, r, "/", http.StatusFound)
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

// groupsFromAccessToken decodes the Keycloak access token (a JWT) and returns
// the `groups` claim. No signature verification is performed — the token was
// received directly from Keycloak's token endpoint over TLS, so its origin is
// already trusted. This is only used as a fallback when the ID token's
// `groups` claim is absent (i.e. the client scope Group Membership mapper
// does not have "Add to ID token" enabled in Keycloak).
func groupsFromAccessToken(accessToken string) []string {
	parts := strings.SplitN(accessToken, ".", 3)
	if len(parts) != 3 {
		return nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil
	}
	var claims struct {
		Groups []string `json:"groups"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil
	}
	return claims.Groups
}

// roleFromGroups maps a user's Keycloak groups to the dashboard's role
// system:
//
//	adminGroup is in groups                → RoleAdmin
//	any group starts with "k8s-"           → RoleViewer
//	none of the above                      → "" (access denied)
//
// The "k8s-" prefix covers all namespace-scoped groups the platform emits
// (k8s-<project>-edit, k8s-<project>-view, k8s-managers-view). Users
// authenticated to Keycloak but with no k8s-* group have no business in the
// dashboard — hence access denied.
func roleFromGroups(groups []string, adminGroup string) string {
	hasK8sGroup := false
	for _, g := range groups {
		if adminGroup != "" && g == adminGroup {
			return RoleAdmin
		}
		if strings.HasPrefix(g, "k8s-") {
			hasK8sGroup = true
		}
	}
	if hasK8sGroup {
		return RoleViewer
	}
	return ""
}

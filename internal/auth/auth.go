// Package auth provides HMAC-cookie session authentication and role-based access control.
//
// Roles:
//   - admin  — full access, admin badge in the UI
//   - viewer — read-only access, view-only badge in the UI
//
// Credentials are loaded from environment variables (see server.go).
// The session token is a base64url-encoded payload signed with HMAC-SHA256.
package auth

// ---------------------------------------------------------------------------
// Author: Labiyb M. Said — DevSecOps Engineer
// Contact: abdulmunimsaid82@gmail.com
// ---------------------------------------------------------------------------

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

const (
	cookieName      = "k8s_session"
	sessionDuration = 24 * time.Hour

	RoleAdmin  = "admin"
	RoleViewer = "viewer"
)

// User is a dashboard account.
type User struct {
	Username string
	Password string // plaintext; constant-time comparison prevents timing attacks
	Role     string // RoleAdmin or RoleViewer
}

// Claims is the identity extracted from a valid session cookie.
type Claims struct {
	Username string
	Role     string
}

type claimsKey struct{}

// GetClaims returns the authenticated user's claims from the request context.
// Returns nil if the middleware has not run (should not happen in normal operation).
func GetClaims(r *http.Request) *Claims {
	c, _ := r.Context().Value(claimsKey{}).(*Claims)
	return c
}

// GenerateSecret returns a cryptographically random 32-byte hex secret.
func GenerateSecret() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("auth: cannot read random bytes: " + err.Error())
	}
	return hex.EncodeToString(b)
}

func sign(payload, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

// createToken encodes a signed, expiring session token for the given user.
// Format: base64url(username NUL role NUL expiry_unix) "." hmac_hex
//
// This is a deliberately minimal hand-rolled format rather than a JWT: no
// library, no JSON, no algorithm-confusion surface — just three NUL-delimited
// fields (usernames/roles can't contain NUL, so this can't be ambiguously
// parsed) and an HMAC over the whole payload. The server is the only verifier,
// so there's nothing to gain from a standardized, third-party-interoperable
// format.
func createToken(username, role, secret string) string {
	exp := fmt.Sprintf("%d", time.Now().Add(sessionDuration).Unix())
	raw := username + "\x00" + role + "\x00" + exp
	payload := base64.RawURLEncoding.EncodeToString([]byte(raw))
	return payload + "." + sign(payload, secret)
}

// parseToken verifies the signature and expiry, returning claims if valid.
func parseToken(token, secret string) (*Claims, bool) {
	dot := strings.LastIndex(token, ".")
	if dot < 0 {
		return nil, false
	}
	payload, sig := token[:dot], token[dot+1:]

	// Constant-time MAC verification
	if !hmac.Equal([]byte(sign(payload, secret)), []byte(sig)) {
		return nil, false
	}

	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return nil, false
	}
	parts := strings.SplitN(string(raw), "\x00", 3)
	if len(parts) != 3 {
		return nil, false
	}

	var exp int64
	fmt.Sscanf(parts[2], "%d", &exp)
	if time.Now().Unix() > exp {
		return nil, false
	}
	return &Claims{Username: parts[0], Role: parts[1]}, true
}

// checkCredentials validates username/password using constant-time comparison.
// Iterates all users unconditionally to prevent timing side-channels.
func checkCredentials(users []User, username, password string) *User {
	var match *User
	for i := range users {
		u := &users[i]
		// No early `return` or `break` on match: every login attempt walks
		// the full user list and runs the same constant-time comparisons,
		// so the response time can't leak *which* user (if any) matched —
		// only the final boolean outcome is observable.
		if hmac.Equal([]byte(u.Username), []byte(username)) &&
			hmac.Equal([]byte(u.Password), []byte(password)) {
			match = u
		}
	}
	return match
}

// Middleware wraps handler, redirecting unauthenticated requests to /login
// and injecting validated Claims into the request context.
func Middleware(next http.Handler, secret string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		// Routes that must stay reachable WITHOUT a session cookie:
		//   /login, /logout    — the auth flow itself (can't require auth to log in)
		//   /healthz, /readyz  — kubelet probes never send cookies; gating these
		//                        would make the pod un-probeable and get it killed
		//   /metrics           — Prometheus scrapers don't carry dashboard sessions
		//   /api/mode          — the login page's "DATA SOURCE" panel fetches this
		//                        before a session exists; gating it just makes the
		//                        fetch 302-redirect back to /login, so the JSON
		//                        parse fails and the panel is stuck on "Unknown"
		//   /auth/login        — initiates the Keycloak OIDC redirect; must be
		//                        reachable without a session (it IS the login flow)
		//   /auth/callback     — Keycloak posts back here; the browser carries no
		//                        dashboard session cookie at this point
		// None of these expose cluster data — see metrics.go for what /metrics
		// actually returns (operational counters only, no namespace/pod detail),
		// and handleMode (handlers.go) for /api/mode (just a {"mock": bool} flag).
		if p == "/login" || p == "/logout" || p == "/embed" || p == "/healthz" || p == "/readyz" || p == "/metrics" || p == "/api/mode" || p == "/auth/login" || p == "/auth/callback" {
			next.ServeHTTP(w, r)
			return
		}
		// /tv/* paths — TV kiosk mode, public read-only. No cluster mutation
		// is possible here (no /tv/export, no POSTs). The TV server is on the
		// internal LAN so this is safe; if exposing externally, gate by source IP.
		if p == "/tv" || strings.HasPrefix(p, "/tv/") {
			next.ServeHTTP(w, r)
			return
		}
		cookie, err := r.Cookie(cookieName)
		if err != nil || cookie.Value == "" {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		claims, ok := parseToken(cookie.Value, secret)
		if !ok {
			// Bad signature, malformed token, or expired — clear the stale
			// cookie so the browser doesn't keep resending it on every request.
			clearSession(w)
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		// claimsKey{} is an unexported empty struct used purely as a context
		// key — guarantees no collision with keys set by other packages
		// (string keys can collide; typed empty-struct keys can't).
		ctx := context.WithValue(r.Context(), claimsKey{}, claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequireAdmin returns HTTP 403 if the authenticated user's role is not admin.
func RequireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if c := GetClaims(r); c == nil || c.Role != RoleAdmin {
			http.Error(w, "forbidden: admin role required", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

// loginSuccessTotal/loginFailureTotal back LoginStats(), exposed via the
// dashboard's /metrics endpoint (internal/api/metrics.go) so sustained growth
// in failures — a brute-force signal — can be alerted on. Package-level
// because there's exactly one auth configuration per process; see
// docs/PRODUCTION_READINESS.md §2.3/§2.5.
var (
	loginSuccessTotal atomic.Int64
	loginFailureTotal atomic.Int64
)

// LoginStats returns the cumulative login attempt counts by outcome since
// process start, for exposition via /metrics.
func LoginStats() (successTotal, failureTotal int64) {
	return loginSuccessTotal.Load(), loginFailureTotal.Load()
}

// HandleLogin serves GET /login (the login page) and processes POST /login (credentials).
func HandleLogin(users []User, secret string) http.HandlerFunc {
	// Read login.html once and substitute {{APP_ENV}} with the environment
	// name (Development / Production) from the env var. Same pattern used for
	// index.html in api.Server. Defaults to "Development" if APP_ENV is unset.
	loginHTML, err := os.ReadFile("web/login.html")
	if err != nil {
		slog.Error("could not read web/login.html — login page will 500", "error", err)
	}
	appEnv := os.Getenv("APP_ENV")
	if appEnv == "" {
		appEnv = "Development"
	}
	loginHTML = bytes.ReplaceAll(loginHTML, []byte("{{APP_ENV}}"), []byte(appEnv))

	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write(loginHTML)
		case http.MethodPost:
			ip := clientIP(r)
			if err := r.ParseForm(); err != nil {
				http.Redirect(w, r, "/login?error=1", http.StatusFound)
				return
			}
			username := r.FormValue("username")
			user := checkCredentials(users, username, r.FormValue("password"))
			if user == nil {
				// Audit log the FAILED attempt — username + remote IP, NEVER
				// the password. This is what lets you later answer "who's
				// hammering the login page, and as whom?" — see
				// docs/PRODUCTION_READINESS.md §2.3.
				loginFailureTotal.Add(1)
				slog.Warn("login failed", "component", "auth", "event", "login_failure",
					"username_attempted", username, "remote_addr", ip)
				http.Redirect(w, r, "/login?error=1", http.StatusFound)
				return
			}
			loginSuccessTotal.Add(1)
			slog.Info("login succeeded", "component", "auth", "event", "login_success",
				"username", user.Username, "role", user.Role, "remote_addr", ip)
			http.SetCookie(w, &http.Cookie{
				Name:     cookieName,
				Value:    createToken(user.Username, user.Role, secret),
				Path:     "/",
				HttpOnly: true,
				SameSite: http.SameSiteLaxMode,
				MaxAge:   int(sessionDuration.Seconds()),
			})
			http.Redirect(w, r, "/", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}
}

// HandleEmbed validates a static embed token and issues a viewer session cookie
// with SameSite=None so cross-site iframes (e.g. a TV wall display) can carry
// it on subsequent requests. The token must be set via the EMBED_TOKEN env var;
// if embedToken is empty, the handler returns 404.
//
// Usage: point an iframe at /embed?token=<EMBED_TOKEN>. The handler sets the
// session cookie and redirects to / — the iframe then behaves like a logged-in
// viewer for the lifetime of the session.
// HandleEmbed validates a static embed token and issues a viewer session cookie
// so cross-origin iframes (e.g. a TV wall display) can stay authenticated.
//
// Cookie strategy depends on how the request arrives:
//   - HTTPS (through Istio/gateway, X-Forwarded-Proto: https): SameSite=None + Secure
//     — required for cross-domain iframe embedding
//   - HTTP (NodePort direct access from TV, no TLS): SameSite=Lax, no Secure
//     — works because the TV page and this service share the same host IP,
//       making them "same-site" even on different ports; Secure would silently
//       drop the cookie over plain HTTP
func HandleEmbed(embedToken, secret string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if embedToken == "" {
			http.NotFound(w, r)
			return
		}
		if !hmac.Equal([]byte(r.URL.Query().Get("token")), []byte(embedToken)) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		https := r.Header.Get("X-Forwarded-Proto") == "https" || r.TLS != nil
		sameSite := http.SameSiteLaxMode
		if https {
			sameSite = http.SameSiteNoneMode
		}
		http.SetCookie(w, &http.Cookie{
			Name:     cookieName,
			Value:    createToken("kiosk", RoleViewer, secret),
			Path:     "/",
			HttpOnly: true,
			Secure:   https,
			SameSite: sameSite,
			MaxAge:   int(sessionDuration.Seconds()),
		})
		http.Redirect(w, r, "/", http.StatusFound)
	}
}

// HandleLogout clears the session cookie and redirects to /login.
func HandleLogout(w http.ResponseWriter, r *http.Request) {
	if c := GetClaims(r); c != nil {
		slog.Info("logout", "component", "auth", "event", "logout", "username", c.Username)
	}
	clearSession(w)
	http.Redirect(w, r, "/login", http.StatusFound)
}

// clientIP extracts the request's source IP for audit-logging and rate-
// limiting purposes. It deliberately uses r.RemoteAddr — NOT the
// X-Forwarded-For / X-Real-IP headers — because those are client-supplied
// and trivially spoofable unless your edge proxy is configured to strip
// client-sent values and overwrite them with the real address. If this app
// runs behind a trusted reverse proxy that does that correctly, switch this
// to read the trusted header instead (and only then).
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func clearSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:    cookieName,
		Value:   "",
		Path:    "/",
		MaxAge:  -1,
		Expires: time.Unix(0, 0),
	})
}

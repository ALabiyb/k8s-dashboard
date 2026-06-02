// Package auth provides HMAC-cookie session authentication and role-based access control.
//
// Roles:
//   - admin  — full access, admin badge in the UI
//   - viewer — read-only access, view-only badge in the UI
//
// Credentials are loaded from environment variables (see server.go).
// The session token is a base64url-encoded payload signed with HMAC-SHA256.
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
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
		if p == "/login" || p == "/logout" {
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
			clearSession(w)
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
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

// HandleLogin serves GET /login (the login page) and processes POST /login (credentials).
func HandleLogin(users []User, secret string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			http.ServeFile(w, r, "web/login.html")
		case http.MethodPost:
			if err := r.ParseForm(); err != nil {
				http.Redirect(w, r, "/login?error=1", http.StatusFound)
				return
			}
			user := checkCredentials(users, r.FormValue("username"), r.FormValue("password"))
			if user == nil {
				http.Redirect(w, r, "/login?error=1", http.StatusFound)
				return
			}
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

// HandleLogout clears the session cookie and redirects to /login.
func HandleLogout(w http.ResponseWriter, r *http.Request) {
	clearSession(w)
	http.Redirect(w, r, "/login", http.StatusFound)
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

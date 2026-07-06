package auth

// ---------------------------------------------------------------------------
// Author: Labiyb M. Said — DevSecOps Engineer
// Contact: abdulmunimsaid82@gmail.com
// ---------------------------------------------------------------------------

// Session storage for the dashboard.
//
// Why server-side, not just a signed cookie:
//   1. Keycloak *access tokens* are JWTs (~2–3 KB) and *refresh tokens* are
//      opaque secrets — neither belongs in a browser cookie that ships on
//      every request. Storing them server-side keeps them off the wire past
//      the initial callback.
//   2. Cookies max out at ~4 KB per site; identity + access + refresh
//      tokens would put us right at the limit and break inevitably.
//   3. Signed self-contained cookies can't be revoked. A server-side store
//      lets logout, and future forced-signout, actually invalidate a
//      session.
//
// Trade-off: sessions live in memory. A dashboard pod restart logs every
// user out (they redirect through Keycloak once). Acceptable for the
// single-pod deployment model; add on-disk or Redis persistence later if we
// scale to multiple replicas.

import (
	"crypto/rand"
	"encoding/base64"
	"log/slog"
	"sync"
	"time"
)

// Session is everything the dashboard knows about a logged-in user for the
// lifetime of their browser cookie.
//
// AccessToken / RefreshToken are populated only for OIDC-authenticated
// sessions. Local username/password logins leave them empty — those users
// have no OIDC identity to forward to Kubernetes.
type Session struct {
	Username     string
	Role         string    // RoleAdmin | RoleViewer
	Groups       []string  // Keycloak groups (nil for local logins)
	AccessToken  string    // OIDC access token (JWT) — forwarded to K8s API
	RefreshToken string    // OIDC refresh token — used to renew AccessToken
	TokenExpiry  time.Time // when AccessToken expires (zero for local logins)
	ExpiresAt    time.Time // when the session itself expires
}

// SessionStore is an in-memory session map keyed by an opaque session ID.
type SessionStore struct {
	mu       sync.RWMutex
	sessions map[string]*Session
}

func NewSessionStore() *SessionStore {
	return &SessionStore{sessions: map[string]*Session{}}
}

// Create stores sess under a freshly-minted session ID and returns the ID.
// The ID is a URL-safe base64 of 32 random bytes — unguessable and safe to
// use as-is as the cookie value (no HMAC signing needed since the ID has no
// meaning and can't be tampered into a different session).
func (s *SessionStore) Create(sess *Session) string {
	id := randomID()
	s.mu.Lock()
	s.sessions[id] = sess
	s.mu.Unlock()
	return id
}

// Get returns the session for id, or nil if the id is unknown, the session
// has expired, or the id is empty. Expired entries are deleted on the fly.
func (s *SessionStore) Get(id string) *Session {
	if id == "" {
		return nil
	}
	s.mu.RLock()
	sess, ok := s.sessions[id]
	s.mu.RUnlock()
	if !ok {
		return nil
	}
	if time.Now().After(sess.ExpiresAt) {
		s.Delete(id)
		return nil
	}
	return sess
}

// Update replaces the stored session for id (used by the refresh flow when
// a new AccessToken/RefreshToken is minted). No-op if id is unknown.
func (s *SessionStore) Update(id string, sess *Session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sessions[id]; ok {
		s.sessions[id] = sess
	}
}

// Delete removes the session for id if present. Safe to call for unknown ids.
func (s *SessionStore) Delete(id string) {
	s.mu.Lock()
	delete(s.sessions, id)
	s.mu.Unlock()
}

// GC removes expired entries. Meant to be called periodically by StartGC or
// by tests. Cheap even with thousands of sessions.
func (s *SessionStore) GC() int {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for id, sess := range s.sessions {
		if now.After(sess.ExpiresAt) {
			delete(s.sessions, id)
			n++
		}
	}
	return n
}

// StartGC runs GC every interval in a background goroutine. Fire-and-forget —
// stops only when the process exits.
func (s *SessionStore) StartGC(interval time.Duration) {
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for range t.C {
			if n := s.GC(); n > 0 {
				slog.Debug("session store GC", "component", "auth", "removed", n)
			}
		}
	}()
}

// randomID returns 32 cryptographically random bytes as URL-safe base64.
// Panics on RNG failure — no useful fallback exists.
func randomID() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("auth: cannot read random bytes: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

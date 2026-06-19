package auth

// ---------------------------------------------------------------------------
// Author: Labiyb M. Said — DevSecOps Engineer
// Contact: abdulmunimsaid82@gmail.com
// ---------------------------------------------------------------------------

import (
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// loginRateLimit/loginRateBurst tune how aggressively /login POSTs are
// throttled per source IP. Picked to be invisible to a real person typing a
// password (a few mistypes within a burst of 5 won't trip it) while making
// scripted brute-forcing impractically slow: at ~5/minute sustained, testing
// even a small password list takes hours instead of seconds. Adjust if real
// usage patterns prove these too strict/loose — see
// docs/PRODUCTION_READINESS.md §2.1.
const (
	loginRateLimit = rate.Limit(5.0 / 60.0) // ~5 sustained attempts per minute
	loginRateBurst = 5                      // allow an initial burst of 5
)

// loginLimiters tracks one token-bucket limiter per source IP. Entries are
// created lazily on first use and swept periodically (sweepLoginLimiters) so
// the map doesn't grow without bound under sustained traffic from many IPs.
var (
	loginLimitersMu sync.Mutex
	loginLimiters   = make(map[string]*loginLimiterEntry)
	sweepStarted    sync.Once
)

type loginLimiterEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// RateLimitLogin wraps a /login handler with a per-source-IP rate limit,
// applied only to POST requests (the credential-check path) — GET requests
// that just render the login page pass through untouched, since those can't
// be used to guess passwords.
//
// On the first call this also starts a background goroutine that sweeps
// stale entries out of the limiter map, so long-running processes don't
// accumulate one entry per distinct IP forever.
func RateLimitLogin(next http.HandlerFunc) http.HandlerFunc {
	sweepStarted.Do(func() { go sweepLoginLimiters() })

	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			next(w, r)
			return
		}
		if !limiterFor(clientIP(r)).Allow() {
			http.Error(w, "too many login attempts — please wait a minute and try again", http.StatusTooManyRequests)
			return
		}
		next(w, r)
	}
}

func limiterFor(ip string) *rate.Limiter {
	loginLimitersMu.Lock()
	defer loginLimitersMu.Unlock()

	e, ok := loginLimiters[ip]
	if !ok {
		e = &loginLimiterEntry{limiter: rate.NewLimiter(loginRateLimit, loginRateBurst)}
		loginLimiters[ip] = e
	}
	e.lastSeen = time.Now()
	return e.limiter
}

// sweepLoginLimiters periodically removes limiter entries that haven't been
// touched in a while — an IP that stops sending login attempts shouldn't keep
// consuming memory indefinitely. Runs for the lifetime of the process.
func sweepLoginLimiters() {
	const (
		interval = 10 * time.Minute
		maxIdle  = 30 * time.Minute
	)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for range ticker.C {
		cutoff := time.Now().Add(-maxIdle)
		loginLimitersMu.Lock()
		for ip, e := range loginLimiters {
			if e.lastSeen.Before(cutoff) {
				delete(loginLimiters, ip)
			}
		}
		loginLimitersMu.Unlock()
	}
}

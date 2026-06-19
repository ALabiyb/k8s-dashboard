// Package api exposes the HTTP endpoints that the frontend dashboard consumes.
//
// Files in this package:
//   server.go   — Server struct, New(), Start(), getenv()
//   poll.go     — background poll loop that keeps data fresh
//   handlers.go — all HTTP handler methods
//   metrics.go  — /metrics, /healthz, /readyz (operational endpoints)
//
// Endpoints:
//   GET /            → login-protected HTML dashboard
//   GET /login       → login page  (POST processes credentials, rate-limited)
//   GET /logout      → clears session, redirects to /login
//   GET /api/summary → current cluster health as JSON  (auth required)
//   GET /api/mode    → mock/real flag for the UI banner (auth required)
//   GET /api/me      → current user's username and role (auth required)
//   GET /api/export  → download snapshot as JSON or CSV (admin only)
//   GET /healthz     → liveness probe   (public — "is the process up")
//   GET /readyz      → readiness probe  (public — "has the first poll landed")
//   GET /metrics     → Prometheus-format operational counters (public)
//
// Auth: HMAC-signed cookie session. Credentials via env vars:
//   ADMIN_USER / ADMIN_PASS   (default: admin / admin)
//   VIEWER_USER / VIEWER_PASS (default: viewer / viewer)
//   DASHBOARD_SECRET          (default: random — sessions lost on restart)
package api

// ---------------------------------------------------------------------------
// Author: Labiyb M. Said — DevSecOps Engineer
// Contact: abdulmunimsaid82@gmail.com
// ---------------------------------------------------------------------------

import (
	"bytes"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"

	"github.com/yourorg/k8s-dashboard/config"
	"github.com/yourorg/k8s-dashboard/internal/aggregator"
	"github.com/yourorg/k8s-dashboard/internal/auth"
	"github.com/yourorg/k8s-dashboard/internal/collector"
	"github.com/yourorg/k8s-dashboard/internal/mock"
	"github.com/yourorg/k8s-dashboard/internal/notifier"
)

// Server holds everything the HTTP handlers and poll loop need.
type Server struct {
	cfg        *config.Config
	collector  *collector.Collector // nil in mock mode
	mockCol    *mock.Collector      // nil in real mode
	aggregator *aggregator.Aggregator
	notifier   *notifier.Notifier
	mockMode   bool
	users      []auth.User
	secret     string
	embedToken string
	oidc       *auth.OIDCHandler // nil when OIDC is disabled in config

	// mu protects summary/ready so the poll goroutine and handlers can share
	// them safely — poll() writes from a background goroutine, handlers read
	// from per-request goroutines.
	mu      sync.RWMutex
	summary aggregator.Summary
	ready   bool // set true after the first poll completes — see /readyz

	// metrics holds plain atomic counters exposed via /metrics. See metrics.go.
	metrics serverMetrics

	// indexHTML is the pre-rendered index.html with APP_ENV substituted in.
	// Built once at startup so every request is a cheap in-memory write.
	indexHTML []byte
}

// New wires up all dependencies and returns a ready-to-run Server.
// Falls back to mock mode automatically when no k8s cluster is reachable.
func New(cfg *config.Config, useMock bool) (*Server, error) {
	agg := aggregator.New(cfg.Thresholds)
	not := notifier.New(cfg.Notifications.Email)

	embedToken := getenv("EMBED_TOKEN", "")
	if embedToken == "" {
		slog.Warn("EMBED_TOKEN not set — /embed endpoint disabled", "component", "auth")
	}

	secret := getenv("DASHBOARD_SECRET", "")
	if secret == "" {
		secret = auth.GenerateSecret()
		slog.Warn("DASHBOARD_SECRET not set — sessions will not survive restarts", "component", "auth")
	}

	adminUser := getenv("ADMIN_USER", "admin")
	adminPass := getenv("ADMIN_PASS", "admin")
	viewerUser := getenv("VIEWER_USER", "viewer")
	viewerPass := getenv("VIEWER_PASS", "viewer")

	if adminPass == "admin" || viewerPass == "viewer" {
		slog.Warn("default credentials in use — set ADMIN_PASS / VIEWER_PASS env vars", "component", "auth")
	}
	users := []auth.User{
		{Username: adminUser, Password: adminPass, Role: auth.RoleAdmin},
		{Username: viewerUser, Password: viewerPass, Role: auth.RoleViewer},
	}
	slog.Info("accounts configured", "component", "auth", "admin_user", adminUser, "viewer_user", viewerUser)

	oidcHandler, err := auth.NewOIDCHandler(cfg.OIDC, secret)
	if err != nil {
		// OIDC init failure is non-fatal: log a warning and continue with local
		// credentials only. This lets the server start even when Keycloak is
		// temporarily unreachable (e.g. cluster not yet up, cert issues during
		// local dev). The SSO button on the login page will redirect to
		// /auth/login, which returns a clear error when oidcHandler is nil.
		slog.Warn("OIDC disabled — falling back to local credentials only",
			"component", "auth", "error", err)
	} else if oidcHandler != nil {
		slog.Info("OIDC enabled", "component", "auth",
			"issuer", cfg.OIDC.IssuerURL, "client_id", cfg.OIDC.ClientID)
	}

	if useMock {
		slog.Warn("mock mode — using fake data, no k8s cluster needed", "component", "server")
		return &Server{cfg: cfg, mockCol: mock.New(), aggregator: agg, notifier: not,
			mockMode: true, users: users, secret: secret, embedToken: embedToken, oidc: oidcHandler}, nil
	}

	col, err := collector.New()
	if err != nil {
		slog.Warn("k8s unavailable — falling back to mock mode", "component", "server", "error", err)
		return &Server{cfg: cfg, mockCol: mock.New(), aggregator: agg, notifier: not,
			mockMode: true, users: users, secret: secret, embedToken: embedToken, oidc: oidcHandler}, nil
	}

	slog.Info("connected to Kubernetes cluster", "component", "server")
	return &Server{cfg: cfg, collector: col, aggregator: agg, notifier: not,
		mockMode: false, users: users, secret: secret, embedToken: embedToken, oidc: oidcHandler}, nil
}

// Start runs the initial poll, launches the background poll goroutine,
// registers all routes, and begins listening on the configured port.
func (s *Server) Start() error {
	s.poll()
	go s.pollLoop()

	// Read index.html once and substitute {{APP_ENV}} with the environment name.
	// Defaults to "Development" so local runs and unset deployments are safe.
	raw, err := os.ReadFile("web/index.html")
	if err != nil {
		return fmt.Errorf("reading web/index.html: %w", err)
	}
	appEnv := os.Getenv("APP_ENV")
	if appEnv == "" {
		appEnv = "Development"
	}
	s.indexHTML = bytes.ReplaceAll(raw, []byte("{{APP_ENV}}"), []byte(appEnv))

	mux := http.NewServeMux()
	// /login is wrapped in RateLimitLogin: a per-IP token bucket that throttles
	// repeated POST attempts (brute-force credential guessing) without
	// affecting normal use — see docs/PRODUCTION_READINESS.md §2.1 and the
	// comment on auth.RateLimitLogin for the exact limits and reasoning.
	mux.HandleFunc("/embed", auth.HandleEmbed(s.embedToken, s.secret))
	mux.HandleFunc("/login", auth.RateLimitLogin(auth.HandleLogin(s.users, s.secret)))
	mux.HandleFunc("/logout", auth.HandleLogout)
	mux.HandleFunc("/api/summary", s.handleSummary)
	mux.HandleFunc("/api/mode", s.handleMode)
	mux.HandleFunc("/api/me", s.handleMe)
	mux.HandleFunc("/api/export", auth.RequireAdmin(s.handleExport))
	mux.HandleFunc("/favicon.svg", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		http.ServeFile(w, r, "web/favicon.svg")
	})
	// OIDC endpoints — public (see bypass list in auth.Middleware).
	// Registered regardless so /auth/login always gives a clear error rather
	// than a 404 when OIDC is disabled or misconfigured.
	if s.oidc != nil {
		mux.HandleFunc("/auth/login", s.oidc.LoginHandler())
		mux.HandleFunc("/auth/callback", s.oidc.CallbackHandler())
	} else {
		mux.HandleFunc("/auth/login", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/login?error=oidc_disabled", http.StatusFound)
		})
	}

	// Operational endpoints — public (see the bypass list in auth.Middleware):
	// kubelet probes and Prometheus scrapers don't carry session cookies.
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/readyz", s.handleReadyz)
	mux.HandleFunc("/metrics", s.handleMetrics)
	mux.HandleFunc("/", s.handleIndex)

	addr := fmt.Sprintf(":%d", s.cfg.Server.Port)
	slog.Info("listening", "component", "server", "addr", addr)
	return http.ListenAndServe(addr, auth.Middleware(mux, s.secret))
}

// getenv returns the value of key, or def when the variable is unset or empty.
func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

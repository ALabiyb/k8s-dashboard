// Package api exposes the HTTP endpoints that the frontend dashboard consumes.
//
// Files in this package:
//   server.go   — Server struct, New(), Start(), getenv()
//   poll.go     — background poll loop that keeps data fresh
//   handlers.go — all HTTP handler methods
//
// Endpoints:
//   GET /            → login-protected HTML dashboard
//   GET /login       → login page  (POST processes credentials)
//   GET /logout      → clears session, redirects to /login
//   GET /api/summary → current cluster health as JSON  (auth required)
//   GET /api/mode    → mock/real flag for the UI banner (auth required)
//   GET /api/me      → current user's username and role (auth required)
//   GET /api/export  → download snapshot as JSON or CSV (admin only)
//
// Auth: HMAC-signed cookie session. Credentials via env vars:
//   ADMIN_USER / ADMIN_PASS   (default: admin / admin)
//   VIEWER_USER / VIEWER_PASS (default: viewer / viewer)
//   DASHBOARD_SECRET          (default: random — sessions lost on restart)
package api

import (
	"fmt"
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

	// mu protects summary so the poll goroutine and handlers can share it safely.
	mu      sync.RWMutex
	summary aggregator.Summary
}

// New wires up all dependencies and returns a ready-to-run Server.
// Falls back to mock mode automatically when no k8s cluster is reachable.
func New(cfg *config.Config, useMock bool) (*Server, error) {
	agg := aggregator.New(cfg.Thresholds)
	not := notifier.New(cfg.Notifications.Email)

	secret := getenv("DASHBOARD_SECRET", "")
	if secret == "" {
		secret = auth.GenerateSecret()
		fmt.Println("[auth] WARNING: DASHBOARD_SECRET not set — sessions will not survive restarts")
	}

	adminUser := getenv("ADMIN_USER", "admin")
	adminPass := getenv("ADMIN_PASS", "admin")
	viewerUser := getenv("VIEWER_USER", "viewer")
	viewerPass := getenv("VIEWER_PASS", "viewer")

	if adminPass == "admin" || viewerPass == "viewer" {
		fmt.Println("[auth] WARNING: default credentials in use — set ADMIN_PASS / VIEWER_PASS env vars")
	}
	users := []auth.User{
		{Username: adminUser, Password: adminPass, Role: auth.RoleAdmin},
		{Username: viewerUser, Password: viewerPass, Role: auth.RoleViewer},
	}
	fmt.Printf("[auth] accounts: admin=%q viewer=%q\n", adminUser, viewerUser)

	if useMock {
		fmt.Println("[server] ⚠  MOCK MODE — using fake data, no k8s cluster needed")
		return &Server{cfg: cfg, mockCol: mock.New(), aggregator: agg, notifier: not,
			mockMode: true, users: users, secret: secret}, nil
	}

	col, err := collector.New()
	if err != nil {
		fmt.Printf("[server] ⚠  k8s unavailable (%v) — falling back to MOCK MODE\n", err)
		return &Server{cfg: cfg, mockCol: mock.New(), aggregator: agg, notifier: not,
			mockMode: true, users: users, secret: secret}, nil
	}

	fmt.Println("[server] connected to Kubernetes cluster ✓")
	return &Server{cfg: cfg, collector: col, aggregator: agg, notifier: not,
		mockMode: false, users: users, secret: secret}, nil
}

// Start runs the initial poll, launches the background poll goroutine,
// registers all routes, and begins listening on the configured port.
func (s *Server) Start() error {
	s.poll()
	go s.pollLoop()

	mux := http.NewServeMux()
	mux.HandleFunc("/login", auth.HandleLogin(s.users, s.secret))
	mux.HandleFunc("/logout", auth.HandleLogout)
	mux.HandleFunc("/api/summary", s.handleSummary)
	mux.HandleFunc("/api/mode", s.handleMode)
	mux.HandleFunc("/api/me", s.handleMe)
	mux.HandleFunc("/api/export", auth.RequireAdmin(s.handleExport))
	mux.HandleFunc("/favicon.svg", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		http.ServeFile(w, r, "web/favicon.svg")
	})
	mux.HandleFunc("/", s.handleIndex)

	addr := fmt.Sprintf(":%d", s.cfg.Server.Port)
	fmt.Printf("[server] listening on http://localhost%s\n", addr)
	return http.ListenAndServe(addr, auth.Middleware(mux, s.secret))
}

// getenv returns the value of key, or def when the variable is unset or empty.
func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

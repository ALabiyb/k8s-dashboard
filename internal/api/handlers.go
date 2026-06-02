package api

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/yourorg/k8s-dashboard/internal/auth"
)

// handleSummary serves the current health summary as JSON.
func (s *Server) handleSummary(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	summary := s.summary
	s.mu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "max-age=15")
	if err := json.NewEncoder(w).Encode(summary); err != nil {
		http.Error(w, "failed to encode response", http.StatusInternalServerError)
	}
}

// handleMode tells the frontend whether we're in mock or real mode.
func (s *Server) handleMode(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"mock": s.mockMode})
}

// handleMe returns the authenticated user's username and role as JSON.
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	claims := auth.GetClaims(r)
	if claims == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"username": claims.Username,
		"role":     claims.Role,
	})
}

// handleIndex serves the single-page HTML dashboard.
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "web/index.html")
}

// handleExport downloads the current health snapshot as JSON or CSV.
// Protected by RequireAdmin — viewers receive HTTP 403.
func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	summary := s.summary
	s.mu.RUnlock()

	switch r.URL.Query().Get("format") {
	case "csv":
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="k8s-health.csv"`)
		cw := csv.NewWriter(w)
		cw.Write([]string{"namespace", "health", "score_pct", "healthy", "total",
			"service", "kind", "status", "reason", "ready", "desired"})
		for _, p := range summary.Products {
			for _, svc := range p.Services {
				cw.Write([]string{
					p.Namespace, string(p.Health),
					fmt.Sprintf("%d", p.ScorePercent),
					fmt.Sprintf("%d", p.HealthyCount),
					fmt.Sprintf("%d", p.TotalCount),
					svc.Name, svc.Kind, svc.Status, svc.Reason,
					fmt.Sprintf("%d", svc.Ready),
					fmt.Sprintf("%d", svc.Desired),
				})
			}
		}
		cw.Flush()
	default:
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", `attachment; filename="k8s-health.json"`)
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		enc.Encode(summary)
	}
}

package api

// ---------------------------------------------------------------------------
// Author: Labiyb M. Said — DevSecOps Engineer
// Contact: abdulmunimsaid82@gmail.com
// ---------------------------------------------------------------------------

import (
	"fmt"
	"net/http"
	"sync/atomic"

	"github.com/ALabiyb/k8s-dashboard/internal/auth"
)

// serverMetrics holds plain atomic counters/gauges updated by poll() (see
// poll.go) and read by handleMetrics below. Deliberately NOT using a metrics
// client library (e.g. prometheus/client_golang) — for a handful of values,
// hand-rolled exposition-format output is simpler than the dependency it'd
// pull in. If the metric surface grows significantly, revisit that tradeoff.
type serverMetrics struct {
	pollTotal      atomic.Int64 // count of completed poll cycles (success or failure)
	pollErrors     atomic.Int64 // count of polls that failed to collect from k8s
	pollDurationMs atomic.Int64 // wall-clock time of the most recent poll, in ms
}

// handleHealthz is the liveness probe: "is the process up and serving HTTP?"
// Always 200 once the server can answer at all — it deliberately does NOT
// check whether polling has succeeded (that's /readyz's job). Conflating the
// two would let a single bad poll cycle get the pod killed and restarted,
// which wouldn't fix anything if the problem is the cluster, not this app.
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

// handleReadyz is the readiness probe: "has the first poll completed, so
// /api/summary has real data to serve?" Returns 503 until then, so a
// freshly-started pod isn't sent traffic before it has anything useful to show
// (matters most right after a deploy/restart, when several replicas might be
// starting up at once).
func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	ready := s.ready
	s.mu.RUnlock()

	if !ready {
		http.Error(w, "not ready: waiting for first poll to complete", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ready\n"))
}

// handleMetrics exposes operational counters in Prometheus text exposition
// format (https://prometheus.io/docs/instrumenting/exposition_formats/) so a
// Prometheus server can scrape this endpoint directly — no sidecar, no
// extra dependency. Covers exactly what's needed to alert on "is polling
// healthy" and "is auth under attack"; extend deliberately, not speculatively.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	summary := s.summary
	s.mu.RUnlock()

	loginSuccess, loginFailure := auth.LoginStats()

	mode := 0.0 // 0 = real, 1 = mock — lets you alert "are we accidentally serving fake data in prod?"
	if s.mockMode {
		mode = 1
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

	metric := func(name, help, typ string, value float64) {
		_, _ = fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n%s %g\n", name, help, name, typ, name, value)
	}

	metric("dashboard_mock_mode", "1 if running against fake/mock data instead of a real cluster, else 0.", "gauge", mode)
	metric("dashboard_poll_total", "Total number of completed poll cycles.", "counter", float64(s.metrics.pollTotal.Load()))
	metric("dashboard_poll_errors_total", "Total number of poll cycles that failed to collect from the cluster.", "counter", float64(s.metrics.pollErrors.Load()))
	metric("dashboard_poll_duration_ms", "Wall-clock duration of the most recent poll cycle, in milliseconds.", "gauge", float64(s.metrics.pollDurationMs.Load()))
	metric("dashboard_namespaces", "Number of namespaces in the most recent poll's summary.", "gauge", float64(len(summary.Products)))
	metric("dashboard_services_total", "Total number of services across all namespaces in the most recent summary.", "gauge", float64(summary.TotalServices))
	metric("dashboard_services_healthy", "Number of healthy services in the most recent summary.", "gauge", float64(summary.HealthyServices))
	metric("dashboard_services_degraded", "Number of degraded services in the most recent summary.", "gauge", float64(summary.DegradedServices))
	metric("dashboard_services_unhealthy", "Number of unhealthy services in the most recent summary.", "gauge", float64(summary.UnhealthyServices))
	metric("dashboard_login_success_total", "Total number of successful login attempts since startup.", "counter", float64(loginSuccess))
	metric("dashboard_login_failure_total", "Total number of failed login attempts since startup — sustained growth may indicate brute-forcing.", "counter", float64(loginFailure))
}

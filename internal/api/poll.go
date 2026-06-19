package api

// ---------------------------------------------------------------------------
// Author: Labiyb M. Said — DevSecOps Engineer
// Contact: abdulmunimsaid82@gmail.com
// ---------------------------------------------------------------------------

import (
	"context"
	"log/slog"
	"time"

	"github.com/yourorg/k8s-dashboard/internal/collector"
)

// pollLoop runs forever, calling poll() on the configured interval.
func (s *Server) pollLoop() {
	ticker := time.NewTicker(s.cfg.Server.PollInterval)
	defer ticker.Stop()
	for range ticker.C {
		s.poll()
	}
}

// poll fetches fresh data (real or mock), aggregates it, and checks for alerts.
func (s *Server) poll() {
	start := time.Now()
	var snapshots []collector.NamespaceSnapshot

	if s.mockMode {
		snapshots = s.mockCol.CollectAll()
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()

		var err error
		snapshots, err = s.collector.CollectAll(ctx, s.cfg.ExcludedNS)
		if err != nil {
			slog.Error("error collecting from k8s", "component", "poll", "error", err)
			s.metrics.pollErrors.Add(1)
			return
		}
	}

	summary := s.aggregator.Aggregate(snapshots)

	if !s.mockMode {
		s.notifier.CheckAndNotify(summary)
	}

	s.mu.Lock()
	s.summary = summary
	// Readiness flips true after the FIRST completed poll — this is what
	// /readyz checks (see handlers.go). Liveness (/healthz) doesn't depend on
	// this; a pod can be "alive" before its first poll completes, just not
	// yet ready to serve meaningful data.
	s.ready = true
	s.mu.Unlock()

	mode := "real"
	if s.mockMode {
		mode = "mock"
	}

	// Record metrics for /metrics (see metrics.go) — plain atomic counters,
	// no client library needed for a handful of gauges/counters.
	duration := time.Since(start)
	s.metrics.pollTotal.Add(1)
	s.metrics.pollDurationMs.Store(duration.Milliseconds())

	slog.Info("poll completed", "component", "poll", "mode", mode,
		"products", len(summary.Products),
		"healthy_services", summary.HealthyServices,
		"total_services", summary.TotalServices,
		"duration_ms", duration.Milliseconds())
}

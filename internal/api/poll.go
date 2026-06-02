package api

import (
	"context"
	"fmt"
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
	var snapshots []collector.NamespaceSnapshot

	if s.mockMode {
		snapshots = s.mockCol.CollectAll()
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()

		var err error
		snapshots, err = s.collector.CollectAll(ctx, s.cfg.ExcludedNS)
		if err != nil {
			fmt.Printf("[poll] error collecting from k8s: %v\n", err)
			return
		}
	}

	summary := s.aggregator.Aggregate(snapshots)

	if !s.mockMode {
		s.notifier.CheckAndNotify(summary)
	}

	s.mu.Lock()
	s.summary = summary
	s.mu.Unlock()

	mode := "real"
	if s.mockMode {
		mode = "mock"
	}
	fmt.Printf("[poll:%s] %d products, %d/%d services healthy\n",
		mode, len(summary.Products), summary.HealthyServices, summary.TotalServices)
}

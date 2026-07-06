// Package aggregator takes raw namespace snapshots from the collector
// and computes a human-friendly health score for each product (namespace).
// This is where the "product is 83% healthy → amber" logic lives.
// If you want to change how scores are calculated, this is the file to edit.
package aggregator

// ---------------------------------------------------------------------------
// Author: Labiyb M. Said — DevSecOps Engineer
// Contact: abdulmunimsaid82@gmail.com
// ---------------------------------------------------------------------------

import (
	"fmt"

	"github.com/ALabiyb/k8s-dashboard/config"
	"github.com/ALabiyb/k8s-dashboard/internal/collector"
)

// HealthLevel is the overall state of a product.
type HealthLevel string

const (
	Healthy  HealthLevel = "Healthy"   // all services OK
	Degraded HealthLevel = "Degraded"  // some services down but above threshold
	Critical HealthLevel = "Critical"  // too many services down
)

// ProductHealth is the aggregated view of one namespace/product.
type ProductHealth struct {
	Namespace     string                  // k8s namespace name, e.g. "ecommerce"
	DisplayName   string                  // friendly name (same as namespace for now)
	Health        HealthLevel             // Healthy / Degraded / Critical
	ScorePercent  int                     // 0–100
	HealthyCount  int                     // number of healthy services
	TotalCount    int                     // total services
	Services      []collector.ServiceState // individual service states
	PreviousHealth HealthLevel            // used by notifier to detect state changes
}

// Summary is the cluster-wide roll-up shown in the top bar.
type Summary struct {
	TotalServices   int
	HealthyServices int
	DegradedServices int
	UnhealthyServices int
	Products        []ProductHealth
}

// Aggregator computes health scores using the configured thresholds.
type Aggregator struct {
	thresholds config.ThresholdConfig
	// previousStates stores the last known HealthLevel per namespace.
	// This lets the notifier know when a state CHANGES (green→amber etc.)
	previousStates map[string]HealthLevel
}

// New creates an Aggregator with the given thresholds from config.yaml.
func New(thresholds config.ThresholdConfig) *Aggregator {
	return &Aggregator{
		thresholds:     thresholds,
		previousStates: make(map[string]HealthLevel),
	}
}

// Aggregate converts a slice of namespace snapshots into a Summary.
// Call this every poll cycle with fresh collector data.
func (a *Aggregator) Aggregate(snapshots []collector.NamespaceSnapshot) Summary {
	summary := Summary{}

	for _, snap := range snapshots {
		product := a.computeProduct(snap)
		summary.Products = append(summary.Products, product)

		// Roll up service counts into the cluster summary.
		//
		// NOTE: svc.Status (per-SERVICE) and product.Health (per-NAMESPACE)
		// are deliberately different enums with different string values —
		// {"Healthy","Degraded","Unhealthy"} vs {"Healthy","Degraded","Critical"}.
		// Don't assume they're interchangeable when reading this code (or the
		// frontend's JSON): a namespace's overall Health is *derived* from a
		// score threshold (scoreToHealth, below), not a roll-up of its
		// services' individual Status strings.
		summary.TotalServices += product.TotalCount
		summary.HealthyServices += product.HealthyCount
		for _, svc := range product.Services {
			switch svc.Status {
			case "Degraded":
				summary.DegradedServices++
			case "Unhealthy":
				summary.UnhealthyServices++
			}
		}
	}

	return summary
}

// computeProduct calculates the health of a single namespace.
func (a *Aggregator) computeProduct(snap collector.NamespaceSnapshot) ProductHealth {
	total := len(snap.Services)
	healthy := 0

	for _, svc := range snap.Services {
		if svc.Status == "Healthy" {
			healthy++
		}
	}

	// Avoid division by zero for empty namespaces
	score := 100
	if total > 0 {
		score = (healthy * 100) / total
	}

	health := a.scoreToHealth(score)

	// Record the previous state before updating, so the notifier can compare
	prev, _ := a.previousStates[snap.Namespace]
	a.previousStates[snap.Namespace] = health

	return ProductHealth{
		Namespace:      snap.Namespace,
		DisplayName:    snap.Namespace, // you can add a display name map in config if you want
		Health:         health,
		ScorePercent:   score,
		HealthyCount:   healthy,
		TotalCount:     total,
		Services:       snap.Services,
		PreviousHealth: prev,
	}
}

// scoreToHealth converts a numeric score to a HealthLevel based on config thresholds.
// To change the thresholds, edit config/config.yaml — no code change needed.
func (a *Aggregator) scoreToHealth(score int) HealthLevel {
	switch {
	case score >= a.thresholds.Healthy:
		return Healthy
	case score >= a.thresholds.Degraded:
		return Degraded
	default:
		return Critical
	}
}

// StateChanged returns true if this product's health is different from last cycle.
// Used by the notifier to decide whether to send an email.
func StateChanged(p ProductHealth) bool {
	// If PreviousHealth is empty string, this is the first poll — don't alert yet.
	if p.PreviousHealth == "" {
		return false
	}
	return p.Health != p.PreviousHealth
}

// FormatStateChange returns a human-readable description of the transition.
// Example: "Analytics: Healthy → Critical"
func FormatStateChange(p ProductHealth) string {
	return fmt.Sprintf("%s: %s → %s", p.DisplayName, p.PreviousHealth, p.Health)
}

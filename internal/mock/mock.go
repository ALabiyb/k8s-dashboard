// Package mock provides a fake Collector that returns realistic dummy data.
// It is used when:
//   - You run locally with no kubeconfig (pure UI testing)
//   - You pass the -mock flag at startup
//
// Health distribution (always visible on the dashboard):
//   Critical  — ecommerce   (3 broken services, score ~62%)
//   Degraded  — analytics, auth  (1 broken each, score ~83-87%)
//   Healthy   — all remaining namespaces
//
// On top of the static failures, one "flapping" service randomly breaks/recovers
// every few polls to simulate real-world churn and trigger state-change alerts.
package mock

import (
	"math/rand"
	"time"

	"github.com/yourorg/k8s-dashboard/internal/collector"
)

// flappingPool is the pool of services that randomly break/recover between polls.
var flappingPool = []string{"ingestion-worker", "billing-deployment", "nginx-gateway"}

// staticIssues defines services that are always broken so the dashboard always
// has an Issues section — makes the demo useful without a real cluster.
var staticIssues = map[string][]struct{ name, status, reason string }{
	// ecommerce: 3 broken out of 8 → 62% healthy → Critical (red)
	"ecommerce": {
		{name: "payment-service",     status: "Unhealthy", reason: "CrashLoopBackOff"},
		{name: "cart-service",        status: "Unhealthy", reason: "OOMKilled"},
		{name: "notification-worker", status: "Degraded",  reason: "1/2 pods ready"},
	},
	// analytics: 1 broken out of 8 → 87% healthy → Degraded (amber)
	"analytics": {
		{name: "ml-pipeline", status: "Unhealthy", reason: "OOMKilled"},
	},
	// auth: 1 broken out of 6 → 83% healthy → Degraded (amber)
	"auth": {
		{name: "oauth-proxy", status: "Unhealthy", reason: "ImagePullBackOff"},
	},
}

var products = []struct {
	namespace string
	deploys   []string
	sts       []string
}{
	{
		namespace: "ecommerce",
		deploys:   []string{"api-gateway", "order-service", "payment-service", "cart-service", "notification-worker"},
		sts:       []string{"postgres", "redis", "kafka"},
	},
	{
		namespace: "analytics",
		deploys:   []string{"api-gateway", "ingestion-worker", "report-service", "ml-pipeline", "scheduler"},
		sts:       []string{"clickhouse", "minio", "kafka"},
	},
	{
		namespace: "auth",
		deploys:   []string{"auth-service", "token-service", "oauth-proxy", "session-manager"},
		sts:       []string{"postgres", "redis"},
	},
	{
		namespace: "logistics",
		deploys:   []string{"tracking-service", "route-optimizer", "notification-worker", "webhook-handler"},
		sts:       []string{"postgres", "kafka"},
	},
	{
		namespace: "payments",
		deploys:   []string{"payment-gateway", "fraud-detector", "invoice-service", "reconciler"},
		sts:       []string{"postgres", "redis"},
	},
	{
		namespace: "notifications",
		deploys:   []string{"email-worker", "sms-worker", "push-worker", "template-service"},
		sts:       []string{"redis", "postgres"},
	},
}

var rng = rand.New(rand.NewSource(time.Now().UnixNano()))

// Collector is the mock implementation — same interface as the real collector.
type Collector struct {
	flapping string // the one service randomly breaking/recovering each poll
}

// New creates a mock Collector with one randomly broken flapping service.
func New() *Collector {
	return &Collector{
		flapping: flappingPool[rng.Intn(len(flappingPool))],
	}
}

// CollectAll returns fake namespace snapshots that look like real k8s data.
func (c *Collector) CollectAll() []collector.NamespaceSnapshot {
	// 15% chance per poll to flip the flapping service — simulates real-world churn
	if rng.Float32() < 0.15 {
		c.flapping = flappingPool[rng.Intn(len(flappingPool))]
	}

	var snapshots []collector.NamespaceSnapshot
	for _, p := range products {
		snap := collector.NamespaceSnapshot{Namespace: p.namespace}
		for _, name := range p.deploys {
			snap.Services = append(snap.Services, c.makeService(p.namespace, name, "Deployment"))
		}
		for _, name := range p.sts {
			snap.Services = append(snap.Services, c.makeService(p.namespace, name, "StatefulSet"))
		}
		snapshots = append(snapshots, snap)
	}
	return snapshots
}

// makeService builds a ServiceState, applying static issues first, then the flapping override.
func (c *Collector) makeService(namespace, name, kind string) collector.ServiceState {
	// Static issues — always broken so the Issues section is always populated
	for _, issue := range staticIssues[namespace] {
		if issue.name == name {
			ready := int32(0)
			if issue.status == "Degraded" {
				ready = 1
			}
			return collector.ServiceState{
				Name:    name,
				Kind:    kind,
				Status:  issue.status,
				Reason:  issue.reason,
				Ready:   ready,
				Desired: 2,
			}
		}
	}

	// Randomly flapping service — breaks and recovers across polls
	if name == c.flapping {
		failures := []struct{ status, reason string }{
			{"Unhealthy", "CrashLoopBackOff"},
			{"Unhealthy", "OOMKilled"},
			{"Degraded", "1/2 pods ready"},
		}
		f := failures[rng.Intn(len(failures))]
		ready := int32(0)
		if f.status == "Degraded" {
			ready = 1
		}
		return collector.ServiceState{
			Name: name, Kind: kind,
			Status: f.status, Reason: f.reason,
			Ready: ready, Desired: 2,
		}
	}

	// Everything else is healthy
	return collector.ServiceState{
		Name:    name,
		Kind:    kind,
		Status:  "Healthy",
		Reason:  "2/2 pods ready",
		Ready:   2,
		Desired: 2,
	}
}

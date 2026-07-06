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

// ---------------------------------------------------------------------------
// Author: Labiyb M. Said — DevSecOps Engineer
// Contact: abdulmunimsaid82@gmail.com
// ---------------------------------------------------------------------------

import (
	"math/rand"
	"time"

	"github.com/ALabiyb/k8s-dashboard/internal/collector"
)

// flappingPool is the pool of services that randomly break/recover between polls.
//
// On each CollectAll(), there's a ~15% chance the *currently* flapping service
// is swapped for a different random pick from this pool (see the rng.Float32
// check in CollectAll). Whichever one is picked gets a random failure mode
// from makeService's `failures` list until it's swapped out again — so over
// enough polls you'll see each of these break, recover, and (because
// rankHealth re-sorts the namespace grid every render) watch its panel jump
// to the top of the dashboard and back. Add a service name here to make its
// namespace demonstrate that behavior; a name with no match anywhere in
// `products` would silently never trigger (a real bug fixed in this list once
// already — keep names in sync with the deploy/sts lists below).
var flappingPool = []string{"ingestion-worker", "checkout-web", "search-api", "queue-worker", "metrics-collector", "catalog-api"}

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
	{
		namespace: "checkout",
		deploys:   []string{"checkout-web", "cart-svc", "pricing-engine", "promo-svc"},
		sts:       []string{"redis", "postgres"},
	},
	{
		namespace: "search",
		deploys:   []string{"search-api", "indexer", "query-router", "suggest-svc"},
		sts:       []string{"elasticsearch", "redis"},
	},
	{
		namespace: "messaging",
		deploys:   []string{"notification-dispatcher", "queue-worker", "webhook-relay"},
		sts:       []string{"kafka", "zookeeper", "redis"},
	},
	{
		namespace: "observability",
		deploys:   []string{"metrics-collector", "log-shipper", "alert-manager"},
		sts:       []string{"prometheus", "loki"},
	},
	{
		namespace: "data-stores",
		deploys:   []string{"db-proxy", "backup-agent"},
		sts:       []string{"postgres-primary", "postgres-replica", "redis-cluster"},
	},
	{
		namespace: "ingress-nginx",
		deploys:   []string{"ingress-controller", "cert-manager", "external-dns"},
		sts:       []string{},
	},
	{
		namespace: "istio-system",
		deploys:   []string{"istiod", "ingress-gateway", "egress-gateway"},
		sts:       []string{},
	},
	{
		namespace: "inventory",
		deploys:   []string{"stock-service", "warehouse-api", "restock-worker"},
		sts:       []string{"postgres"},
	},
	{
		namespace: "shipping",
		deploys:   []string{"shipping-api", "label-printer", "carrier-sync"},
		sts:       []string{"postgres", "redis"},
	},
	{
		namespace: "marketing",
		deploys:   []string{"campaign-service", "email-scheduler", "segment-builder"},
		sts:       []string{"postgres"},
	},
	{
		namespace: "recommendations",
		deploys:   []string{"reco-engine", "feature-store", "ranking-service"},
		sts:       []string{"redis", "postgres"},
	},
	{
		namespace: "support",
		deploys:   []string{"ticket-service", "chat-gateway", "kb-search"},
		sts:       []string{"postgres", "elasticsearch"},
	},
	{
		namespace: "billing",
		deploys:   []string{"billing-api", "invoice-generator", "payment-reconciler"},
		sts:       []string{"postgres"},
	},
	{
		namespace: "catalog",
		deploys:   []string{"catalog-api", "image-processor", "price-sync"},
		sts:       []string{"postgres", "redis"},
	},

	// ── SoftNet project namespaces ────────────────────────────────────────
	// These mirror real dev-cluster namespaces (see the K8s RBAC Bindings
	// runbook) so an OIDC login as e.g. `k8s-softaml-edit` in mock mode can
	// verify that the filter shows only that user's namespace(s).
	{
		namespace: "softaml",
		deploys:   []string{"aml-ingest", "screening-worker", "rules-engine", "case-manager", "reporting-api"},
		sts:       []string{"postgres", "redis"},
	},
	{
		namespace: "softcms",
		deploys:   []string{"cms-api", "editor-web", "media-worker", "search-indexer"},
		sts:       []string{"postgres", "elasticsearch"},
	},
	{
		namespace: "xm113",
		deploys:   []string{"xm113-orchestrator", "batch-runner", "notify-worker"},
		sts:       []string{"postgres"},
	},
	{
		namespace: "softid",
		deploys:   []string{"identity-api", "kyc-verifier", "document-scanner"},
		sts:       []string{"postgres", "redis"},
	},
	{
		namespace: "soft-guarantee",
		deploys:   []string{"guarantee-api", "policy-engine", "claims-processor"},
		sts:       []string{"postgres"},
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

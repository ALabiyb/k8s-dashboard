// Package collector connects to the Kubernetes API and fetches
// the current state of all workloads in each namespace.
// It works both locally (via ~/.kube/config) and inside the cluster
// (via the pod's ServiceAccount token) — the same binary handles both.
package collector

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/flowcontrol"
)

// ServiceState holds the health status of one k8s workload (Deployment or StatefulSet).
type ServiceState struct {
	Name      string // e.g. "order-service"
	Kind      string // "Deployment" or "StatefulSet"
	Namespace string // e.g. "softcms"
	Status    string // "Healthy", "Degraded", or "Unhealthy"
	Reason    string // human-readable reason, e.g. "CrashLoopBackOff" or "2/3 pods ready"
	Ready     int32  // number of ready replicas right now
	Desired   int32  // number of replicas that should be running
}

// NamespaceSnapshot is everything we know about one namespace at a point in time.
// One snapshot = one product card on the dashboard.
type NamespaceSnapshot struct {
	Namespace string
	Services  []ServiceState
}

// Collector wraps the Kubernetes client and exposes CollectAll.
type Collector struct {
	client *kubernetes.Clientset
}

// New creates a Collector.
// It tries in-cluster config first (when running as a pod inside k8s),
// then falls back to your local ~/.kube/config (when running on your laptop).
// No flags needed — it detects the environment automatically.
func New() (*Collector, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		// Not inside a cluster — use local kubeconfig
		kubeconfig := os.Getenv("KUBECONFIG")
		if kubeconfig == "" {
			// Default path on Linux/macOS/Windows
			home, _ := os.UserHomeDir()
			kubeconfig = filepath.Join(home, ".kube", "config")
		}
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			return nil, fmt.Errorf("cannot build k8s config: %w", err)
		}
	}

	// ── Rate limiter fix ──────────────────────────────────────────────────────
	// The default client-go rate limit is 5 requests/second.
	// With 20+ namespaces collected in parallel, this caused:
	//   "client rate limiter Wait returned an error: context deadline exceeded"
	// Raising it to 50 req/s (burst 100) fixes this completely.
	// If you have 50+ namespaces and still see errors, raise these numbers further.
	config.RateLimiter = flowcontrol.NewTokenBucketRateLimiter(50, 100)

	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("cannot create k8s client: %w", err)
	}

	return &Collector{client: client}, nil
}

// CollectAll fetches snapshots for every namespace except those in excludedNS.
//
// KEY CHANGE FROM v1: namespaces are now collected CONCURRENTLY (in parallel).
// The old sequential approach took 20+ seconds for 20 namespaces and timed out.
// Now all namespaces are fetched at the same time, finishing in ~2-3 seconds.
func (c *Collector) CollectAll(ctx context.Context, excludedNS []string) ([]NamespaceSnapshot, error) {
	// Build a quick lookup set so we can skip excluded namespaces in O(1)
	excluded := make(map[string]bool, len(excludedNS))
	for _, ns := range excludedNS {
		excluded[ns] = true
	}

	// List all namespaces in the cluster
	nsList, err := c.client.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing namespaces: %w", err)
	}

	// Build the list of namespaces we actually want to collect
	var namespacesToCollect []string
	for _, ns := range nsList.Items {
		if !excluded[ns.Name] {
			namespacesToCollect = append(namespacesToCollect, ns.Name)
		}
	}

	// ── Concurrent collection ─────────────────────────────────────────────────
	// We spin up one goroutine per namespace. A mutex protects the shared
	// snapshots slice, and a WaitGroup lets us know when all goroutines are done.
	var (
		mu        sync.Mutex       // protects snapshots slice from concurrent writes
		snapshots []NamespaceSnapshot
		wg        sync.WaitGroup
	)

	// Semaphore limits how many goroutines run at once.
	// 10 means at most 10 namespaces are queried simultaneously.
	// Raise this if your cluster is fast; lower it if you see API pressure.
	semaphore := make(chan struct{}, 10)

	for _, ns := range namespacesToCollect {
		wg.Add(1)
		go func(namespace string) {
			defer wg.Done()

			// Acquire a semaphore slot — this blocks if 10 goroutines are already running
			semaphore <- struct{}{}
			defer func() { <-semaphore }() // release slot when done

			// Each namespace gets its own 15s timeout.
			// This means one slow namespace won't block or cancel all the others.
			nsCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()

			snap, err := c.collectNamespace(nsCtx, namespace)
			if err != nil {
				// Log the error and skip this namespace — don't abort the whole poll
				fmt.Printf("[collector] error collecting %s: %v\n", namespace, err)
				return
			}

			// Safely append to the shared slice
			mu.Lock()
			snapshots = append(snapshots, snap)
			mu.Unlock()
		}(ns)
	}

	// Block until every goroutine has finished
	wg.Wait()

	return snapshots, nil
}

// collectNamespace fetches Deployments, StatefulSets, and Pods from one namespace.
// This is called once per namespace, inside a goroutine.
func (c *Collector) collectNamespace(ctx context.Context, ns string) (NamespaceSnapshot, error) {
	snap := NamespaceSnapshot{Namespace: ns}

	// ── Deployments (your app services) ──────────────────────────────────────
	deployList, err := c.client.AppsV1().Deployments(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return snap, fmt.Errorf("listing deployments: %w", err)
	}
	for _, d := range deployList.Items {
		snap.Services = append(snap.Services, deploymentToState(d))
	}

	// ── StatefulSets (postgres, redis, kafka, minio, rabbitmq, etc.) ──────────
	stsList, err := c.client.AppsV1().StatefulSets(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return snap, fmt.Errorf("listing statefulsets: %w", err)
	}
	for _, s := range stsList.Items {
		snap.Services = append(snap.Services, statefulSetToState(s))
	}

	// ── Pods: second pass to detect crash-level problems ─────────────────────
	// Deployment/StatefulSet replica counts tell us IF something is wrong,
	// but not WHY. We read individual pod statuses to get the actual reason:
	// CrashLoopBackOff, ImagePullBackOff, OOMKilled, etc.
	podList, err := c.client.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return snap, fmt.Errorf("listing pods: %w", err)
	}
	enrichWithPodProblems(snap.Services, podList.Items)

	return snap, nil
}

// deploymentToState converts a k8s Deployment object into our ServiceState.
// Replica counts determine the initial health status before pod-level enrichment.
func deploymentToState(d appsv1.Deployment) ServiceState {
	desired := int32(1) // k8s defaults to 1 replica if not explicitly set
	if d.Spec.Replicas != nil {
		desired = *d.Spec.Replicas
	}
	ready := d.Status.ReadyReplicas

	svc := ServiceState{
		Name:      d.Name,
		Kind:      "Deployment",
		Namespace: d.Namespace,
		Ready:     ready,
		Desired:   desired,
	}
	svc.Status, svc.Reason = healthFromReplicas(ready, desired)
	return svc
}

// statefulSetToState converts a k8s StatefulSet into our ServiceState.
// Same logic as deploymentToState — StatefulSets use the same replica model.
func statefulSetToState(s appsv1.StatefulSet) ServiceState {
	desired := int32(1)
	if s.Spec.Replicas != nil {
		desired = *s.Spec.Replicas
	}
	ready := s.Status.ReadyReplicas

	svc := ServiceState{
		Name:      s.Name,
		Kind:      "StatefulSet",
		Namespace: s.Namespace,
		Ready:     ready,
		Desired:   desired,
	}
	svc.Status, svc.Reason = healthFromReplicas(ready, desired)
	return svc
}

// healthFromReplicas maps replica counts to a health status string.
//
//	all ready  → Healthy   (green)
//	some ready → Degraded  (amber)  e.g. 2/3 pods running
//	none ready → Unhealthy (red)    e.g. 0/3 pods running
func healthFromReplicas(ready, desired int32) (status, reason string) {
	switch {
	case desired == 0:
		// Intentionally scaled to zero (e.g. a paused service) — treat as healthy
		return "Healthy", "Scaled to zero (intentional)"
	case ready == desired:
		return "Healthy", fmt.Sprintf("%d/%d pods ready", ready, desired)
	case ready > 0:
		return "Degraded", fmt.Sprintf("%d/%d pods ready", ready, desired)
	default:
		return "Unhealthy", fmt.Sprintf("0/%d pods ready", desired)
	}
}

// enrichWithPodProblems upgrades a service's status based on what its pods are doing.
// It catches failure reasons that replica counts alone don't reveal:
//   - CrashLoopBackOff  — container keeps crashing and restarting
//   - OOMKilled         — container ran out of memory
//   - ImagePullBackOff  — can't pull the container image (wrong tag, no access, etc.)
//   - ErrImagePull      — same as above, first attempt
//   - CreateContainerError — container couldn't be created
//   - Init:*            — an init container is stuck (shown as Degraded, not Unhealthy)
func enrichWithPodProblems(services []ServiceState, pods []corev1.Pod) {
	// Build a name → index map so we can update services in O(1)
	idx := make(map[string]int, len(services))
	for i, s := range services {
		idx[s.Name] = i
	}

	for _, pod := range pods {
		// Find which Deployment/StatefulSet owns this pod (via labels)
		ownerName := podOwnerName(pod)
		if ownerName == "" {
			continue // pod has no recognisable owner label — skip
		}
		i, ok := idx[ownerName]
		if !ok {
			continue // owner not in our service list — skip
		}

		// ── Check regular container statuses ─────────────────────────────────
		for _, cs := range pod.Status.ContainerStatuses {
			if cs.State.Waiting != nil {
				switch cs.State.Waiting.Reason {
				case "CrashLoopBackOff", "OOMKilled", "Error",
					"ImagePullBackOff", "ErrImagePull", "CreateContainerError":
					// Upgrade to Unhealthy and record the specific reason
					services[i].Status = "Unhealthy"
					services[i].Reason = cs.State.Waiting.Reason
				}
			}
		}

		// ── Check init container statuses ────────────────────────────────────
		// Init containers run before the main container starts.
		// If one is stuck, the pod never starts — show it as Degraded.
		for _, cs := range pod.Status.InitContainerStatuses {
			if cs.State.Waiting != nil {
				reason := cs.State.Waiting.Reason
				// "PodInitializing" is normal during startup — ignore it
				if reason != "" && reason != "PodInitializing" {
					// Only downgrade to Degraded if we haven't already marked it Unhealthy
					if services[i].Status != "Unhealthy" {
						services[i].Status = "Degraded"
						services[i].Reason = "Init: " + reason
					}
				}
			}
		}
	}
}

// podOwnerName finds the workload name that owns this pod by checking its labels.
// Most k8s deployments (including Helm charts) set one of these label keys.
// If none match, we return "" and skip the pod.
func podOwnerName(pod corev1.Pod) string {
	for _, key := range []string{
		"app.kubernetes.io/name", // Helm standard
		"app",                    // most custom deployments
		"name",                   // older convention
	} {
		if v, ok := pod.Labels[key]; ok {
			return v
		}
	}
	return ""
}
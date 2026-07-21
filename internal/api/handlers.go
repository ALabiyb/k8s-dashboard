package api

// ---------------------------------------------------------------------------
// Author: Labiyb M. Said — DevSecOps Engineer
// Contact: abdulmunimsaid82@gmail.com
// ---------------------------------------------------------------------------

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"

	"github.com/ALabiyb/k8s-dashboard/internal/aggregator"
	"github.com/ALabiyb/k8s-dashboard/internal/auth"
	"github.com/ALabiyb/k8s-dashboard/internal/k8s"
)

const (
	hdrContentType  = "Content-Type"
	hdrCacheControl = "Cache-Control"
	mimeJSON        = "application/json"
)

// handleSummary serves the current health summary as JSON, filtered by the
// caller's Keycloak groups so users only see namespaces their K8s RBAC
// allows.
//
// Filtering rules (see internal/auth/groups.go and the "K8s Dashboard OIDC
// RBAC Migration" runbook):
//
//	admin group / k8s-managers-view → full cluster view (no filter)
//	k8s-<ns>-edit or k8s-<ns>-view  → only that namespace
//	no session (TV kiosk path)      → full cluster view — the TV is a public
//	                                  overview and shouldn't hide anything
//	no k8s-* group                  → empty view (Products list empty)
func (s *Server) handleSummary(w http.ResponseWriter, r *http.Request) {
	// Read the cached summary under a read-lock — it's overwritten wholesale
	// by poll() (in poll.go) every poll_interval from a different goroutine,
	// so concurrent reads/writes need this mutex (s.mu) to be race-free.
	s.mu.RLock()
	summary := s.summary
	s.mu.RUnlock()

	// Only filter when the caller is an OIDC-authenticated user (identified by
	// a non-empty Groups list). Local username/password logins carry no
	// groups and keep the historical "see everything" behavior — same as
	// pre-migration. The TV kiosk path has no claims at all and skips this
	// block entirely.
	if claims := auth.GetClaims(r); claims != nil && len(claims.Groups) > 0 {
		summary = filterSummaryForUser(summary, claims.Groups, s.oidcAdminGroup)
	}

	w.Header().Set(hdrContentType, mimeJSON)
	// Short client-side cache: smooths out bursts of near-simultaneous
	// requests (e.g. several browser tabs polling) without serving data
	// that's meaningfully stale relative to the 30s poll_interval.
	w.Header().Set(hdrCacheControl, "max-age=15")
	if err := json.NewEncoder(w).Encode(summary); err != nil {
		http.Error(w, "failed to encode response", http.StatusInternalServerError)
	}
}

// filterSummaryForUser returns a Summary containing only the namespaces the
// user's groups grant access to, and re-derives the top-bar service counts
// from that filtered view. When the user has cluster-wide access, the input
// is returned unchanged.
func filterSummaryForUser(full aggregator.Summary, userGroups []string, adminGroup string) aggregator.Summary {
	allowAll, allowed := auth.AllowedNamespaces(userGroups, adminGroup)
	if allowAll {
		return full
	}
	out := aggregator.Summary{}
	for _, p := range full.Products {
		if !allowed[p.Namespace] {
			continue
		}
		out.Products = append(out.Products, p)
		for _, svc := range p.Services {
			out.TotalServices++
			switch svc.Status {
			case "Healthy":
				out.HealthyServices++
			case "Degraded":
				out.DegradedServices++
			case "Unhealthy":
				out.UnhealthyServices++
			}
		}
	}
	return out
}

// handleMode tells the frontend whether we're in mock or real mode.
func (s *Server) handleMode(w http.ResponseWriter, r *http.Request) {
	w.Header().Set(hdrContentType, mimeJSON)
	_ = json.NewEncoder(w).Encode(map[string]bool{"mock": s.mockMode})
}

// handleMe returns the authenticated user's username, role, and (for OIDC
// logins) the raw Keycloak groups so the frontend can render affordances
// gated by group membership. Groups is an empty array for local logins.
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	claims := auth.GetClaims(r)
	if claims == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	groups := claims.Groups
	if groups == nil {
		groups = []string{} // encode as [], not null
	}
	w.Header().Set(hdrContentType, mimeJSON)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"username": claims.Username,
		"role":     claims.Role,
		"groups":   groups,
	})
}

// handleIndex serves the single-page HTML dashboard with APP_ENV substituted.
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set(hdrContentType, "text/html; charset=utf-8")
	_, _ = w.Write(s.indexHTML)
}

// handleTVMe returns a fixed kiosk identity for the no-auth TV mode. The
// frontend uses it only to populate the header chip and to hide admin
// affordances — never as a security check (the real check is which endpoints
// the kiosk path is allowed to hit, which is enforced server-side).
func (s *Server) handleTVMe(w http.ResponseWriter, r *http.Request) {
	w.Header().Set(hdrContentType, mimeJSON)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"username": "tv",
		"role":     "viewer",
	})
}

// handleExport downloads the current health snapshot as JSON or CSV.
// Protected by RequireAdmin — viewers receive HTTP 403.
func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	summary := s.summary
	s.mu.RUnlock()

	switch r.URL.Query().Get("format") {
	case "csv":
		w.Header().Set(hdrContentType, "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="k8s-health.csv"`)
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{"namespace", "health", "score_pct", "healthy", "total",
			"service", "kind", "status", "reason", "ready", "desired"})
		for _, p := range summary.Products {
			for _, svc := range p.Services {
				_ = cw.Write([]string{
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
		if err := cw.Error(); err != nil {
			slog.WarnContext(r.Context(), "csv export write error", "component", "api", "error", err)
		}
	default:
		w.Header().Set(hdrContentType, mimeJSON)
		w.Header().Set("Content-Disposition", `attachment; filename="k8s-health.json"`)
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(summary)
	}
}

// handlePodList returns the pods in a namespace that match one of the
// well-known owner labels used by the collector (`app.kubernetes.io/name`,
// `app`, `name`). The frontend uses this to resolve a Deployment/StatefulSet
// name to the actual pod name(s) it should stream logs from.
//
// Route:  GET /api/pods/{ns}/list?owner=<deployment-or-statefulset-name>
//
// Authorization: enforced by K8s RBAC via the user's OIDC token.
func (s *Server) handlePodList(w http.ResponseWriter, r *http.Request) {
	ns := r.PathValue("ns")
	owner := r.URL.Query().Get("owner")
	if ns == "" || owner == "" {
		http.Error(w, "namespace and owner required", http.StatusBadRequest)
		return
	}
	cs, err := s.userK8sClient(r)
	if err != nil {
		if errors.Is(err, errMockMode) {
			http.Error(w, "pod listing unavailable in mock mode", http.StatusServiceUnavailable)
		} else {
			writeAuthError(w, err)
		}
		return
	}
	pods, err := listPodsForOwner(r.Context(), cs, ns, owner)
	if err != nil {
		writeK8sError(w, err, ns, owner)
		return
	}

	type podLite struct {
		Name       string   `json:"name"`
		Phase      string   `json:"phase"`
		Containers []string `json:"containers"`
	}
	out := make([]podLite, 0, len(pods))
	for _, p := range pods {
		names := make([]string, 0, len(p.Spec.Containers))
		for _, c := range p.Spec.Containers {
			names = append(names, c.Name)
		}
		out = append(out, podLite{
			Name:       p.Name,
			Phase:      string(p.Status.Phase),
			Containers: names,
		})
	}
	w.Header().Set(hdrContentType, mimeJSON)
	w.Header().Set(hdrCacheControl, "no-store")
	_ = json.NewEncoder(w).Encode(out)
}

// listPodsForOwner finds pods in ns belonging to owner. It first tries
// label-based lookup (fast, common case), then falls back to walking
// ownerReferences (robust — handles Rook Ceph OSDs, custom controllers, etc.).
func listPodsForOwner(ctx context.Context, cs kubernetes.Interface, ns, owner string) ([]corev1.Pod, error) {
	for _, key := range []string{"app.kubernetes.io/name", "app", "name"} {
		list, err := cs.CoreV1().Pods(ns).List(ctx, metaListOpts(key+"="+owner))
		if err != nil {
			return nil, err
		}
		if len(list.Items) > 0 {
			return list.Items, nil
		}
	}
	// Fallback: list all pods and walk ownerReferences up one hop.
	all, err := cs.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var pods []corev1.Pod
	for _, p := range all.Items {
		if podOwnedBy(ctx, cs, ns, p, owner) {
			pods = append(pods, p)
		}
	}
	return pods, nil
}

// podOwnedBy returns true if the pod's ownership chain resolves to a workload
// named `owner`. Walks up to one intermediate hop:
//
//	Pod → ReplicaSet → Deployment (owner name matches Deployment)
//	Pod → StatefulSet | DaemonSet | Job (owner name matches directly)
//
// Any error at an intermediate step is treated as "not owned" — the caller
// falls through and the pod is simply not listed. Errors are noisy but
// non-fatal (an admin can still see the same info via kubectl).
func podOwnedBy(ctx context.Context, cs kubernetes.Interface, ns string, p corev1.Pod, owner string) bool {
	for _, ref := range p.OwnerReferences {
		// Direct match — StatefulSet, DaemonSet, Job, or any resource whose
		// name we're already searching for.
		if ref.Name == owner {
			return true
		}
		// ReplicaSet → Deployment (single hop). ReplicaSet names look like
		// "<deployment>-<hash>" but we don't string-match — we look up the
		// RS and check its OwnerReferences directly.
		if ref.Kind == "ReplicaSet" {
			rs, err := cs.AppsV1().ReplicaSets(ns).Get(ctx, ref.Name, metav1.GetOptions{})
			if err != nil {
				continue
			}
			for _, rsRef := range rs.OwnerReferences {
				if rsRef.Name == owner {
					return true
				}
			}
		}
	}
	return false
}

// metaListOpts wraps a labelSelector in the metav1.ListOptions shape without
// pulling metav1 into every file that touches it.
func metaListOpts(labelSelector string) metav1.ListOptions {
	return metav1.ListOptions{LabelSelector: labelSelector}
}

// handleRestartDeployment triggers a rolling restart of a Deployment or
// StatefulSet by patching the pod template with the
// `kubectl.kubernetes.io/restartedAt` annotation — the same mechanism
// `kubectl rollout restart` uses.
//
// Route:  POST /api/workloads/{ns}/{kind}/{name}/restart
//
//	kind ∈ {deployment, statefulset}
//
// Verb required: `patch deployments` or `patch statefulsets`
// (edit-nodelete has this — see K8s RBAC Bindings runbook §5.1).
// Authorization is enforced by K8s RBAC via the user's OIDC token.
func (s *Server) handleRestartWorkload(w http.ResponseWriter, r *http.Request) {
	ns := r.PathValue("ns")
	kind := strings.ToLower(r.PathValue("kind"))
	name := r.PathValue("name")
	if ns == "" || kind == "" || name == "" {
		http.Error(w, "namespace, kind and name required", http.StatusBadRequest)
		return
	}
	if kind != "deployment" && kind != "statefulset" {
		http.Error(w, "kind must be 'deployment' or 'statefulset'", http.StatusBadRequest)
		return
	}

	// Mock mode: pretend the restart succeeded so the UI can be tested
	// end-to-end without a real cluster. No collector side-effect.
	if s.mockMode {
		slog.Info("mock: simulated restart", "component", "api",
			"user", claimsUsername(r), "ns", ns, "kind", kind, "name", name)
		w.Header().Set(hdrContentType, mimeJSON)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":          true,
			"mock":        true,
			"kind":        kind,
			"name":        name,
			"namespace":   ns,
			"restartedAt": time.Now().UTC().Format(time.RFC3339),
		})
		return
	}

	cs, err := s.userK8sClient(r)
	if err != nil {
		writeAuthError(w, err)
		return
	}

	// The patch: set spec.template.metadata.annotations
	// `kubectl.kubernetes.io/restartedAt` to now. K8s notices the pod
	// template changed and rolls out fresh pods. Same mechanism as
	// `kubectl rollout restart deployment/foo`.
	patch := fmt.Sprintf(
		`{"spec":{"template":{"metadata":{"annotations":{"kubectl.kubernetes.io/restartedAt":"%s"}}}}}`,
		time.Now().UTC().Format(time.RFC3339),
	)

	ctx := r.Context()
	var patchErr error
	switch kind {
	case "deployment":
		_, patchErr = cs.AppsV1().Deployments(ns).Patch(
			ctx, name, k8stypes.StrategicMergePatchType, []byte(patch),
			metav1.PatchOptions{},
		)
	case "statefulset":
		_, patchErr = cs.AppsV1().StatefulSets(ns).Patch(
			ctx, name, k8stypes.StrategicMergePatchType, []byte(patch),
			metav1.PatchOptions{},
		)
	}
	if patchErr != nil {
		writeK8sError(w, patchErr, ns, name)
		return
	}

	slog.Info("workload restarted", "component", "api",
		"user", claimsUsername(r), "ns", ns, "kind", kind, "name", name)

	w.Header().Set(hdrContentType, mimeJSON)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":          true,
		"kind":        kind,
		"name":        name,
		"namespace":   ns,
		"restartedAt": time.Now().UTC().Format(time.RFC3339),
	})
}

// handleDeletePod deletes a single pod. Kubernetes recreates it
// automatically if it's part of a Deployment/StatefulSet.
//
// Route:  DELETE /api/pods/{ns}/{name}
//
// Verb required: `delete pods` — only cluster-admin (via k8s-cluster-admins
// group) has this in our RBAC model. Namespace-editors get 403 from the
// apiserver, surfaced here as 403 with a friendly message.
func (s *Server) handleDeletePod(w http.ResponseWriter, r *http.Request) {
	ns := r.PathValue("ns")
	name := r.PathValue("name")
	if ns == "" || name == "" {
		http.Error(w, "namespace and pod name required", http.StatusBadRequest)
		return
	}

	// Mock mode: simulate the delete for UI testing (no real pod exists).
	if s.mockMode {
		slog.Info("mock: simulated pod delete", "component", "api",
			"user", claimsUsername(r), "ns", ns, "pod", name)
		w.Header().Set(hdrContentType, mimeJSON)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":        true,
			"mock":      true,
			"deleted":   name,
			"namespace": ns,
		})
		return
	}

	cs, err := s.userK8sClient(r)
	if err != nil {
		writeAuthError(w, err)
		return
	}

	// Default grace period (30s). Callers can override with ?force=true which
	// sets grace-period=0 for a fast delete — useful when a pod is wedged.
	opts := metav1.DeleteOptions{}
	if r.URL.Query().Get("force") == "true" {
		var zero int64
		opts.GracePeriodSeconds = &zero
	}

	if err := cs.CoreV1().Pods(ns).Delete(r.Context(), name, opts); err != nil {
		writeK8sError(w, err, ns, name)
		return
	}

	slog.Info("pod deleted", "component", "api",
		"user", claimsUsername(r), "ns", ns, "pod", name,
		"force", r.URL.Query().Get("force") == "true")

	w.Header().Set(hdrContentType, mimeJSON)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":        true,
		"deleted":   name,
		"namespace": ns,
	})
}

// userK8sClient centralises the boilerplate that every write-action handler
// needs: verify the request has an OIDC session, refresh the access token
// if needed, and build a per-request K8s client that authenticates AS the
// human. Returns a sentinel error the caller can pass to writeAuthError.
func (s *Server) userK8sClient(r *http.Request) (*kubernetes.Clientset, error) {
	if s.mockMode {
		return nil, errMockMode
	}
	if s.oidc == nil {
		return nil, errNoOIDC
	}
	sessionID := auth.SessionIDFrom(r)
	if sessionID == "" {
		return nil, errNoSession
	}
	token, err := s.oidc.ValidAccessToken(r.Context(), sessionID)
	if err != nil {
		return nil, err
	}
	return k8s.ForUser(token)
}

// Sentinel errors for auth pre-checks. writeAuthError maps them to HTTP.
var (
	errMockMode  = errors.New("mock mode: cluster mutation unavailable")
	errNoOIDC    = errors.New("OIDC not configured on this server")
	errNoSession = errors.New("no active session")
)

func writeAuthError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errMockMode):
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
	case errors.Is(err, errNoOIDC):
		http.Error(w, "requires OIDC authentication", http.StatusForbidden)
	case errors.Is(err, errNoSession):
		http.Error(w, "unauthenticated", http.StatusUnauthorized)
	case errors.Is(err, auth.ErrNoOIDCSession):
		http.Error(w, "not an OIDC session; sign in via Keycloak", http.StatusForbidden)
	case errors.Is(err, auth.ErrRefreshFailed):
		http.Error(w, "session expired; sign in again", http.StatusUnauthorized)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// handlePodLogs streams container logs for a specific pod.
//
// Route (Go 1.22+ path patterns):
//
//	GET /api/pods/{ns}/{name}/logs
//
// Query parameters:
//
//	container   optional; first container if omitted
//	tail        optional int; last N lines (default 200)
//	follow      optional bool; keeps streaming new lines until the client disconnects
//	previous    optional bool; logs of the last terminated container (for crash debugging)
//
// Authorization: enforced by Kubernetes RBAC via the user's OIDC token.
// A namespace-editor for softaml calling /api/pods/other-ns/... gets 403
// from the apiserver, which we surface as a friendly JSON error. There is
// NO additional server-side namespace filter here — RBAC is authoritative.
func (s *Server) handlePodLogs(w http.ResponseWriter, r *http.Request) {
	ns := r.PathValue("ns")
	name := r.PathValue("name")
	if ns == "" || name == "" {
		http.Error(w, "namespace and pod name required", http.StatusBadRequest)
		return
	}
	cs, err := s.userK8sClient(r)
	if err != nil {
		switch {
		case errors.Is(err, errMockMode):
			http.Error(w, "log streaming unavailable in mock mode", http.StatusServiceUnavailable)
		case errors.Is(err, errNoOIDC):
			http.Error(w, "log streaming requires OIDC authentication (not available for local sessions)", http.StatusForbidden)
		default:
			writeAuthError(w, err)
		}
		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	stream, err := cs.CoreV1().Pods(ns).GetLogs(name, parsePodLogOpts(r)).Stream(ctx)
	if err != nil {
		writeK8sError(w, err, ns, name)
		return
	}
	defer stream.Close()

	w.Header().Set(hdrContentType, "text/plain; charset=utf-8")
	w.Header().Set(hdrCacheControl, "no-store")
	w.Header().Set("X-Accel-Buffering", "no") // disable nginx buffering if fronted by one
	w.WriteHeader(http.StatusOK)
	copyLogStream(ctx, w, stream, ns, name)
}

// parsePodLogOpts builds PodLogOptions from the request query string.
func parsePodLogOpts(r *http.Request) *corev1.PodLogOptions {
	q := r.URL.Query()
	tail := int64(200)
	if v := q.Get("tail"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
			tail = n
		}
	}
	opts := &corev1.PodLogOptions{
		Container: q.Get("container"),
		Follow:    q.Get("follow") == "true" || q.Get("follow") == "1",
		Previous:  q.Get("previous") == "true" || q.Get("previous") == "1",
	}
	if tail > 0 {
		opts.TailLines = &tail
	}
	return opts
}

// copyLogStream reads from stream and writes to w, flushing after each chunk
// so the browser sees log lines as they arrive. Returns when the stream ends
// or the client disconnects.
func copyLogStream(ctx context.Context, w http.ResponseWriter, stream io.ReadCloser, ns, name string) {
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 4096)
	for {
		n, readErr := stream.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return // client disconnected
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if readErr != nil {
			if readErr != io.EOF {
				slog.WarnContext(ctx, "pod log stream error",
					"component", "api", "ns", ns, "pod", name, "error", readErr)
			}
			return
		}
	}
}

// writeK8sError maps common Kubernetes API errors to appropriate HTTP
// responses. RBAC denials show up as 403 with a body the frontend can
// display verbatim; missing pods show up as 404; anything else is a 502
// (upstream problem) with a redacted-ish message.
func writeK8sError(w http.ResponseWriter, err error, ns, name string) {
	msg := err.Error()
	switch {
	case containsAny(msg, "forbidden", "cannot get resource"):
		http.Error(w, fmt.Sprintf("access denied for %s/%s (RBAC)", ns, name), http.StatusForbidden)
	case containsAny(msg, "not found", "NotFound"):
		http.Error(w, fmt.Sprintf("pod %s/%s not found", ns, name), http.StatusNotFound)
	case containsAny(msg, "unauthorized", "Unauthorized"):
		http.Error(w, "authentication rejected by apiserver", http.StatusUnauthorized)
	default:
		http.Error(w, "kubernetes error: "+msg, http.StatusBadGateway)
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// claimsUsername returns the authenticated username or "" if the request
// has no claims attached (e.g. TV kiosk path). Used by mock-mode stubs
// where we still want an audit-log line but haven't run the real auth
// pre-check.
func claimsUsername(r *http.Request) string {
	c := auth.GetClaims(r)
	if c == nil {
		return ""
	}
	return c.Username
}

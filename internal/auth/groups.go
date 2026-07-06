package auth

// ---------------------------------------------------------------------------
// Author: Labiyb M. Said — DevSecOps Engineer
// Contact: abdulmunimsaid82@gmail.com
// ---------------------------------------------------------------------------

import "strings"

// clusterWideViewer is the well-known Keycloak group whose members get
// cluster-wide read access in the dashboard, mirroring the K8s ClusterRoleBinding
// `k8s-managers-view` → built-in `view` ClusterRole (see K8s RBAC Bindings runbook).
const clusterWideViewer = "k8s-managers-view"

// AllowedNamespaces returns the set of namespaces the user (identified by
// their Keycloak groups) is allowed to see in the dashboard.
//
// Rules:
//
//	adminGroup is in groups                → allowAll=true (full cluster view)
//	"k8s-managers-view" is in groups       → allowAll=true (cluster-wide read)
//	"k8s-<ns>-edit" or "k8s-<ns>-view"     → ns added to the set
//	no matching group                      → empty set (user sees nothing)
//
// allowAll short-circuits the namespace comparison entirely — useful because
// the dashboard doesn't know the full namespace list at claim-check time and
// we don't want to lock cluster-wide users out just because a new namespace
// appears mid-session.
//
// adminGroup defaults to "k8s-cluster-admins" when the empty string is passed
// (cheap safety net for a mis-configured config.yaml).
func AllowedNamespaces(groups []string, adminGroup string) (allowAll bool, namespaces map[string]bool) {
	if adminGroup == "" {
		adminGroup = "k8s-cluster-admins"
	}
	namespaces = map[string]bool{}
	for _, g := range groups {
		// Cluster-wide personas — return early, no need to enumerate.
		if g == adminGroup || g == clusterWideViewer {
			return true, nil
		}
		// Project-scoped: k8s-<ns>-edit or k8s-<ns>-view. Only strip the k8s-
		// prefix + role suffix; a group like "k8s-managers-view" is already
		// handled above (equals match), so the suffix parse below never sees it.
		if ns := parseProjectGroup(g); ns != "" {
			namespaces[ns] = true
		}
	}
	return false, namespaces
}

// parseProjectGroup extracts the namespace name from a k8s-<ns>-edit or
// k8s-<ns>-view group. Returns "" for anything that doesn't match either
// pattern (or has an empty ns segment).
func parseProjectGroup(g string) string {
	const prefix = "k8s-"
	if !strings.HasPrefix(g, prefix) {
		return ""
	}
	inner := g[len(prefix):]
	for _, suffix := range []string{"-edit", "-view"} {
		if strings.HasSuffix(inner, suffix) {
			ns := inner[:len(inner)-len(suffix)]
			if ns != "" {
				return ns
			}
		}
	}
	return ""
}

// Package k8s provides per-request Kubernetes clients that authenticate as
// the logged-in dashboard user (not the pod's ServiceAccount).
//
// This is the mechanical enabler for RBAC — every user action reaches the
// API server as `user in group X`, so the existing ClusterRoleBindings /
// RoleBindings (see the "K8s RBAC Bindings" Obsidian runbook) decide yes/no.
//
// Compare with internal/collector, which owns the *background* SA-based
// client used for the global health poll — that client is not per-user and
// is not built here.
package k8s

// ---------------------------------------------------------------------------
// Author: Labiyb M. Said — DevSecOps Engineer
// Contact: abdulmunimsaid82@gmail.com
// ---------------------------------------------------------------------------

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// baseConfig returns a REST config with the API server URL + CA bundle only.
// All credential fields are zeroed — the caller is expected to plug in a
// user access token via ForUser. Cached because building it walks the
// filesystem (kubeconfig case).
var (
	baseCfgOnce sync.Once
	baseCfg     *rest.Config
	baseCfgErr  error
)

func loadBaseConfig() (*rest.Config, error) {
	baseCfgOnce.Do(func() {
		// Try in-cluster first (production case: dashboard pod → apiserver).
		c, err := rest.InClusterConfig()
		if err != nil {
			// Fall back to local kubeconfig for laptop dev runs.
			kubeconfig := os.Getenv("KUBECONFIG")
			if kubeconfig == "" {
				home, _ := os.UserHomeDir()
				kubeconfig = filepath.Join(home, ".kube", "config")
			}
			c, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
			if err != nil {
				baseCfgErr = fmt.Errorf("cannot build k8s base config: %w", err)
				return
			}
		}
		// Strip every credential field. We keep only what's needed to *reach*
		// the API server (host + CA); the user's OIDC token, injected later
		// by ForUser, is the sole credential each request carries.
		baseCfg = &rest.Config{
			Host:            c.Host,
			APIPath:         c.APIPath,
			TLSClientConfig: c.TLSClientConfig,
			// Deliberately not copied: BearerToken, BearerTokenFile, Username,
			// Password, AuthProvider, ExecProvider, Impersonate, RateLimiter.
			// If ForUser accidentally left BearerTokenFile populated, client-go
			// would re-read the pod's SA token on every request and silently
			// impersonate the SA instead of the user — the #1 subtle bug in
			// this pattern.
		}
	})
	return baseCfg, baseCfgErr
}

// ForUser returns a Kubernetes clientset that authenticates every API call
// with the supplied OIDC access token. The apiserver validates the token
// against Keycloak (via its `--oidc-*` flags) and applies RBAC based on the
// user's `groups` claim.
//
// accessToken should be a non-empty JWT obtained from the SoftNet AD realm
// via the `k8s-dashboard` client (which stamps `k8s` into the token's `aud`
// so the apiserver accepts it). Returns an error if the token is empty or
// the base config cannot be built.
func ForUser(accessToken string) (*kubernetes.Clientset, error) {
	if accessToken == "" {
		return nil, fmt.Errorf("k8s: empty access token — cannot build per-user client")
	}
	base, err := loadBaseConfig()
	if err != nil {
		return nil, err
	}
	// Copy the base by value so multiple concurrent callers don't race on
	// BearerToken. The struct is small and shallow-copyable — TLSClientConfig
	// itself isn't mutated per-request.
	cfg := *base
	cfg.BearerToken = accessToken
	cfg.BearerTokenFile = "" // safety belt; see loadBaseConfig comment
	return kubernetes.NewForConfig(&cfg)
}

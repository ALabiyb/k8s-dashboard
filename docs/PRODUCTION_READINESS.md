# Production Readiness Plan

This is an ordered, actionable plan for taking the dashboard from its current
state (great for demos / internal use) to something safe to expose to real
users on a real cluster. Items are grouped by urgency — work top to bottom.

> The core architecture (stateless server, in-memory polling, signed-cookie
> sessions, no database) is already production-appropriate. The gaps below are
> almost entirely about **secrets, transport security, and observability** —
> not structural rework.

---

## Phase 1 — Must fix before any real exposure

These are the items the app itself is already warning you about at startup
(`[auth] WARNING: ...`), plus the two things that would let an attacker walk
straight in.

> **Status**: the plumbing for 1.1 / 1.2 / 1.3 is now in place — see
> `k8s/k8s/01-secret.example.yaml`, the `secretKeyRef` env entries in
> `k8s/k8s/02-deployment.yaml`, and the `SMTP_PASSWORD` override in
> `config/config.go:Load()`. **What remains is purely operational**: someone
> with cluster access has to actually *create* the `dashboard-secrets` Secret
> with real generated values (the example file explains how, and recommends
> Sealed Secrets / External Secrets Operator for a GitOps-safe flow — plain
> `kubectl create secret` + manual apply works too, just don't ever `git add`
> a copy of it). 1.4 is documented with a ready-to-hand-off reference
> (`k8s/gateway-tls.example.yaml`) but needs the Gateway owner to apply it.

### 1.1 Set a real `DASHBOARD_SECRET`
- **Why it matters**: `internal/api/server.go:59-63` — if unset, the server
  generates a random secret on every boot. Every restart invalidates all
  sessions, and (more importantly) a predictable/empty secret lets anyone forge
  a signed `k8s_session` cookie and become `admin`.
- **What to do**: generate a long random value (`openssl rand -hex 32`) and
  create the `dashboard-secrets` Secret with it under the key
  `DASHBOARD_SECRET` — see `k8s/k8s/01-secret.example.yaml` for the exact
  `kubectl create secret` command and the GitOps-safe alternatives (Sealed
  Secrets / External Secrets Operator). The Deployment already wires this key
  to the `DASHBOARD_SECRET` env var (`k8s/k8s/02-deployment.yaml`) — nothing
  else to change once the Secret exists with a real value.
- **Verify**: the `[auth] WARNING: DASHBOARD_SECRET not set` log line disappears
  on boot, and sessions survive a pod restart.

### 1.2 Replace default credentials
- **Why it matters**: `internal/api/server.go:65-71` ships with `admin`/`admin`
  and `viewer`/`viewer` and prints a warning if you haven't overridden them.
  These are public knowledge (they're in this repo's README).
- **What to do**: set real, unique values for `ADMIN_USER` / `ADMIN_PASS` /
  `VIEWER_USER` / `VIEWER_PASS` in the same `dashboard-secrets` Secret
  (`k8s/k8s/01-secret.example.yaml` has the keys and command), generated
  per-environment — dev/staging/prod should NOT share credentials. The
  Deployment already wires all four keys to their env vars. Or — better —
  skip this whole local-credential model and go straight to Keycloak/OIDC
  (see `docs/ARCHITECTURE.md` §7.3 for the integration point), which removes
  password management from this app entirely.
- **Verify**: the `[auth] WARNING: default credentials in use` log line
  disappears on boot.

### 1.3 Move the SMTP password out of `config.yaml`
- **Why it matters**: `config/config.yaml` / `k8s/k8s/01-configmap.yaml` had
  `smtp_password: "your-app-password"` in plaintext, and the README
  acknowledged this was a problem ("left as an exercise").
- **Status — done**: `config/config.go:Load()` now reads `SMTP_PASSWORD` from
  the environment and overrides whatever's in the YAML when it's set; the
  ConfigMap's `smtp_password` is now just an inert placeholder
  (`overridden-by-SMTP_PASSWORD-env-var`) with a comment explaining why; and
  the Deployment wires `SMTP_PASSWORD` from `dashboard-secrets`. **Remaining
  step**: put the real SMTP app-password into that Secret (same command as
  1.1/1.2 — see `k8s/k8s/01-secret.example.yaml`).
- **Verify**: `git grep -i smtp_password` in the config files returns only the
  placeholder string, never a real credential — and the dashboard still sends
  alert emails after the Secret is populated with the real value.

### 1.4 Put TLS in front of the dashboard
- **Why it matters**: `internal/api/server.go:118` calls
  `http.ListenAndServe` — plain HTTP. Login POSTs (and the session cookie)
  travel in cleartext over the network.
- **Status — documented, needs the Gateway owner**: `main-gateway`
  (`istio-system`) is a *shared* resource this repo doesn't own or contain —
  TLS is configured on the Gateway's listener, not on `04-httproute.yaml`
  (Gateway API routes can't carry TLS config themselves). I added
  `k8s/gateway-tls.example.yaml`, a ready-to-hand-off reference showing the
  cert-manager `Certificate` + Gateway HTTPS listener needed, plus a
  pointer comment at the top of `04-httproute.yaml`. Hand that file to
  whoever administers `main-gateway` (or apply it yourself if that's you).
- **What to do**: get the Gateway owner to apply the two pieces in
  `k8s/gateway-tls.example.yaml` (adjusting `issuerRef` to your real
  cert-manager `ClusterIssuer`). Once HTTPS is confirmed working end-to-end,
  add `Strict-Transport-Security` at the gateway and set the `Secure` flag on
  the `k8s_session` cookie in `internal/auth/auth.go`.
- **Verify**: `curl -I https://k8s-dashboard.<env>.softnethq.co.tz` returns a
  valid TLS handshake and a certificate issued by your configured issuer; the
  session cookie is served with `Secure` once that's confirmed.

---

## Phase 2 — Strongly recommended before broad rollout

> **Status**: 2.1, 2.3, 2.4, and 2.5 are now implemented in code —
> `internal/auth/ratelimit.go` (per-IP token-bucket limiter on `POST /login`,
> wired up in `internal/api/server.go:Start`), the audit-log + counter additions
> in `internal/auth/auth.go:HandleLogin/HandleLogout` (`slog.Warn`/`slog.Info`
> events plus `loginSuccessTotal`/`loginFailureTotal`), `/healthz` and `/readyz`
> in `internal/api/metrics.go` (wired through `auth.Middleware`'s public-route
> bypass list), and the `log/slog` migration + `/metrics` Prometheus endpoint
> spanning `cmd/server/main.go`, `internal/api/*.go`, `internal/auth/auth.go`,
> `internal/notifier/notifier.go`, and `internal/collector/collector.go`. What
> remains is purely operational: point a Prometheus server at `/metrics`, wire
> `/healthz`/`/readyz` into the Deployment's probe specs (see
> `k8s/k8s/02-deployment.yaml`), and set up alerting on
> `dashboard_login_failure_total` / `dashboard_poll_errors_total`. **2.2 has no
> code fix** — it's a process-discipline guideline (see below) since the
> current design already enforces server-side admin checks on the one
> admin-only endpoint (`/api/export`).

### 2.1 Add login rate limiting / lockout
- **Why it matters**: `checkCredentials` in `internal/auth/auth.go` is
  timing-attack-resistant (`hmac.Equal`) but has no brute-force protection — an
  attacker can try passwords as fast as the network allows.
- **What to do**: add a simple per-IP (or per-username) sliding-window limiter
  in front of `/login` — e.g. `golang.org/x/time/rate` keyed by remote address,
  or push this responsibility to the ingress/gateway (many support rate limiting
  natively, e.g. NGINX ingress annotations).
- **Verify**: scripted rapid-fire login attempts get throttled (e.g. `429 Too
  Many Requests`) after a small threshold.

### 2.2 Close the server-side role-enforcement gap
- **Why it matters**: today only `/api/export` is wrapped in
  `auth.RequireAdmin` (`internal/api/server.go:109`). The drill-down modal is
  gated **client-side only** (`currentRole === 'admin'` checks in
  `web/index.html`). That's currently safe because no endpoint serves
  privileged data the viewer can't already see via `/api/summary` — but it's a
  trap waiting for the next admin-only feature to be added without a matching
  `RequireAdmin` wrapper.
- **What to do**: adopt a rule — *every new route that should be admin-only
  gets wrapped in `auth.RequireAdmin` at registration time, full stop, before
  any UI work happens*. Consider adding a small route-table test that asserts
  the expected wrapper is present for a known list of admin routes, so this
  can't silently regress.
- **Verify**: a viewer session (`curl` with the viewer cookie) gets `403` from
  every endpoint that's supposed to be admin-only — not just `/api/export`.

### 2.3 Add audit logging for security-relevant events
- **Why it matters**: right now you can't answer "who logged in, who exported
  data, and when" — `internal/auth/auth.go` and `handleExport` just do the
  action with no record.
- **What to do**: log structured events (see 2.5) for: login success/failure
  (with username + remote IP, never password), logout, and admin actions
  (export). Ship these to your log aggregator with retention matching your
  org's audit policy.
- **Verify**: a login attempt, a failed login, and an export each produce a
  distinguishable, parseable log line with actor + timestamp + outcome.

### 2.4 Add health/readiness probes for the dashboard itself
- **Why it matters**: if this runs in Kubernetes (it does — see
  `k8s/k8s/02-deployment.yaml`), the platform needs a way to know when the pod
  is alive vs. ready to serve traffic, so it can restart hung pods and avoid
  routing to ones still polling their first cycle.
- **What to do**: add `GET /healthz` (liveness — "is the process up") and `GET
  /readyz` (readiness — "has the first poll completed and is the collector
  reachable") to `internal/api/server.go`'s route table, and reference them in
  the Deployment's `livenessProbe`/`readinessProbe`.
- **Verify**: `kubectl describe pod` shows both probes passing once the
  container is up and has completed its first poll.

### 2.5 Switch to structured logging + add a metrics endpoint
- **Why it matters**: current logs are `fmt.Printf`-style free text (`[main]
  config loaded | port: 8081 | poll: 30s`) — fine for `docker compose logs`,
  painful to query/alert on at scale. There's also no way to see poll latency,
  K8s API error rates, or login failure rates over time.
- **What to do**: swap `fmt.Printf` for a structured logger (`log/slog` is in
  the standard library as of Go 1.21+, no new dependency needed) emitting JSON
  to stdout. Add a Prometheus `/metrics` endpoint (`promhttp` handler) exposing
  poll duration, poll error count, login success/failure counters, and HTTP
  request counts — this slots naturally into the `observability`/`prometheus`
  stack already modeled in the mock namespaces.
- **Verify**: logs are valid JSON lines with consistent fields
  (`timestamp`, `level`, `msg`, …), and `curl localhost:8081/metrics` returns
  Prometheus exposition format.

---

## Phase 3 — Hardening and polish

> **Status**: 3.2, 3.3, and 3.4 are resolved. **3.2** turned out to already be
> covered — the `Jenkinsfile` runs two image-vulnerability stages
> (`vulnScanDocker`, `vulnScanApplicationImage`, both Trivy-based per the
> shared library and the build-flow diagram in `docs/ARCHITECTURE.md`) plus
> SBOM generation and DefectDojo publishing; nothing to add. **3.3**: reviewed
> `internal/collector/collector.go` against `k8s/k8s/06-clusterrole.yaml` —
> the collector only ever calls `.List()` (never `.Get()`/`.Watch()`/informers),
> so the role's `get`/`watch` verbs were unused; trimmed to `list`-only across
> all three rules, with a comment pointing at the grep that proves it. The
> `ClusterRole` (vs. namespaced `Role`s) is correctly scoped — the app
> dynamically discovers namespaces via `Namespaces().List`, which a namespaced
> `Role` can't grant. **3.4**: resource `requests`/`limits` were already set in
> `k8s/k8s/02-deployment.yaml`; while there, also replaced the liveness probe
> (which pointed at the *authenticated* `/api/summary` and only "worked"
> because kubelet treats the 302→`/login` redirect as success) with proper
> `/healthz`/`/readyz` probes — closing the exact operational gap called out in
> the Phase 2 status note. **No PodDisruptionBudget was added**: with
> `replicas: 1`, a PDB's `minAvailable` can never be satisfied during a
> voluntary eviction, so it would only block node drains, not protect anything
> — see the comment above `replicas: 1` for the add-it-when-you-scale guidance.
>
> **3.1 and 3.5 remain deliberately deferred** — both are explicitly written
> in this doc as "do this *when* a triggering condition occurs" (3.1: the first
> state-changing admin action beyond login/logout; 3.5: a real compliance/
> offboarding-SLA requirement for early session revocation). Neither condition
> exists yet, and building either now would be exactly the kind of speculative
> abstraction this project avoids — they're correctly captured as "watch for
> the trigger," not "implement preemptively."

### 3.1 Add CSRF protection to state-changing requests
- **Why it matters**: `/login` and `/logout` are POST/GET forms relying on
  `SameSite=Lax` cookies for partial protection. That's reasonable today (no
  other state-changing endpoints exist), but if you add admin write actions
  later (acknowledge alerts, silence namespaces, etc.), you'll want a real CSRF
  token.
- **What to do**: when you add the first state-changing admin action beyond
  login/logout, add a per-session CSRF token (double-submit cookie pattern is
  simplest given the stateless design) and validate it on every POST.
- **Verify**: a cross-origin form POST to a protected action is rejected.

### 3.2 Scan container images in the CI pipeline
- **Why it matters**: `Jenkinsfile` already builds and pushes images, but
  there's no vulnerability gate before they ship.
- **What to do**: add a Trivy (or Grype) scan stage after the build step,
  failing the pipeline on `HIGH`/`CRITICAL` findings (with an allowlist
  mechanism for accepted-risk CVEs). This is a small addition with high payoff.
- **Verify**: introduce a known-vulnerable base image temporarily and confirm
  the pipeline fails the build.

### 3.3 Verify least-privilege RBAC for the dashboard's ServiceAccount
- **Why it matters**: `k8s/k8s/06-clusterrole.yaml` /
  `07-clusterrolebinding.yaml` define what the dashboard itself can read from
  the cluster it's monitoring. Over-broad permissions here turn a dashboard bug
  into a cluster-wide read (or worse) incident.
- **What to do**: review the `ClusterRole` and confirm it's strictly
  **read-only** (`get`, `list`, `watch` — no `create`/`update`/`delete`/`patch`)
  and scoped to only the resource types the collector actually needs
  (Deployments, StatefulSets, Pods, Events — check
  `internal/collector/collector.go` for the exact API calls it makes). Prefer
  per-namespace `Role`/`RoleBinding`s over a `ClusterRole` if you don't actually
  need cluster-wide visibility.
- **Verify**: `kubectl auth can-i create pods --as=system:serviceaccount:k8s-dashboard:<sa-name>`
  returns `no`, and similarly for every other write verb.

### 3.4 Add resource limits and a PodDisruptionBudget
- **Why it matters**: without `resources.requests`/`limits`, a misbehaving pod
  can starve its node; without a `PodDisruptionBudget`, a voluntary disruption
  (node drain, cluster upgrade) can take down all replicas at once.
- **What to do**: set sane CPU/memory `requests` and `limits` in
  `k8s/k8s/02-deployment.yaml` based on observed usage, and — if you run more
  than one replica (the stateless design supports this cleanly) — add a
  `PodDisruptionBudget` requiring at least one pod to remain available.
- **Verify**: `kubectl describe deployment` shows resource requests/limits;
  `kubectl get pdb` shows the budget; a simulated node drain leaves at least one
  pod serving traffic.

### 3.5 Decide on a session-revocation strategy
- **Why it matters**: signed stateless cookies (`internal/auth/auth.go`) can't
  be revoked early — a session is valid for the full `sessionDuration` (24h)
  even if the underlying account is disabled or the secret rotates mid-session.
  For most internal dashboards this is an acceptable tradeoff; for a
  customer-facing or highly-regulated deployment, it might not be.
- **What to do**: if early revocation is required, you'd need *some* shared
  state — e.g. a short-lived denylist of revoked token IDs in Redis, checked in
  `auth.Middleware`. This is the one place a lightweight datastore might
  actually earn its place in this otherwise-stateless app. Don't add it
  speculatively — only if a real requirement (compliance, offboarding SLA)
  demands it.
- **Verify**: disabling an account (or rotating the secret) takes effect within
  your required SLA, not just at next-login.

---

## Quick reference — what's already fine, leave it alone

- **No database** — the in-memory polling + stateless-cookie design is correct
  for this app's scope and scales horizontally without coordination. Don't add
  one unless you need historical trends, audit storage, or session revocation
  (3.5) — and even then, reach for the smallest thing that solves *that*
  specific need.
- **Timing-safe credential comparison** — `checkCredentials` already uses
  `hmac.Equal`, resistant to timing attacks.
- **HttpOnly + SameSite=Lax cookies** — already set on `k8s_session`, a solid
  baseline against XSS cookie theft and basic CSRF.
- **Stateless server design** — any replica can serve any request; this is
  exactly what you want for horizontal scaling and rolling deploys.

# Architecture & How It Works

A guided tour of the codebase — read this before diving into the source so you
know what each piece is responsible for and how a request flows through the
system end to end.

---

## 1. The big picture

```
┌─────────────┐   poll every 30s    ┌──────────────┐    scores it    ┌─────────────┐
│  Kubernetes  │ ───────────────────▶│  collector   │ ───────────────▶│ aggregator  │
│  API server  │   (or mock data)    │ (raw status) │  (Healthy/      │ (per-product│
└─────────────┘                      └──────────────┘   Degraded/     │  health %)  │
                                                          Critical)    └──────┬──────┘
                                                                              │
                                            ┌─────────────────────────────────┤
                                            │                                 │
                                            ▼                                 ▼
                                     ┌─────────────┐                  ┌──────────────┐
                                     │  notifier   │                  │  api.Server  │
                                     │ (email on   │                  │ (HTTP +      │
                                     │  state      │                  │  /api/*      │
                                     │  change)    │                  │  + auth)     │
                                     └─────────────┘                  └──────┬───────┘
                                                                              │ JSON
                                                                              ▼
                                                                    ┌──────────────────┐
                                                                    │  web/index.html  │
                                                                    │  (poll + render) │
                                                                    └──────────────────┘
```

Everything is **in-memory and stateless** — there is no database. Each poll
cycle re-derives the full picture from scratch (or from the K8s API directly,
or from `internal/mock` if no cluster is reachable), so any replica of this
server can answer any request identically. This is what makes the app trivial
to scale horizontally.

---

## 2. Component-by-component

### `cmd/server/main.go` — entrypoint
Parses flags (`-config`, `-mock`), loads `config/config.yaml`, builds the
`api.Server`, and starts it. About 40 lines — start here to see the boot
sequence in order.

### `config/` — configuration loading
Defines the `Config` struct and `Load(path)`, which reads `config.yaml`
(server port, poll interval, excluded namespaces, health thresholds, SMTP
settings). Plain YAML, no env-var overrides today (see
[`docs/PRODUCTION_READINESS.md`](PRODUCTION_READINESS.md) §1.3 for why that
matters for the SMTP password specifically).

### `internal/collector` — talks to Kubernetes
`Collector.CollectAll()` returns a `[]NamespaceSnapshot`, where each snapshot
is one namespace's raw workload state: every Deployment/StatefulSet's
`Status` (`Healthy`/`Degraded`/`Unhealthy`), `Reason` (e.g.
`CrashLoopBackOff`), and `Ready`/`Desired` replica counts.

- `New()` auto-detects environment: tries in-cluster config first (when
  running as a pod, using the ServiceAccount token), falls back to
  `~/.kube/config` (when running on a laptop). Same binary, no flags needed.
- It deliberately raises the client-go rate limiter above the 5 req/s default
  — see the comment block in `collector.go` explaining the
  `context deadline exceeded` issue this fixes when polling 20+ namespaces in
  parallel.

### `internal/mock` — fake data generator
A drop-in replacement for `Collector` with the exact same `CollectAll()`
shape, used when `-mock` is passed or no cluster is reachable. Useful for UI
work and demos without touching a real cluster. It defines:
- `products` — the list of fake namespaces and their services (currently 20)
- `staticIssues` — services that are *permanently* broken in specific
  namespaces, so the dashboard always has something interesting to show
- `flappingPool` — services that randomly break/recover each poll (~15%
  chance), to simulate real-world churn and exercise the alert-ticker /
  re-sort behavior

### `internal/aggregator` — turns raw status into a health score
Takes the raw `[]NamespaceSnapshot` and produces a `Summary`: for each
namespace, it computes `ScorePercent` (= healthy services ÷ total services)
and maps that to a `HealthLevel` (`Healthy`/`Degraded`/`Critical`) using the
thresholds from `config.yaml` (`thresholds.healthy` / `thresholds.degraded`).
It also tracks `previousStates` per namespace so the notifier can detect
*transitions* (green→amber, amber→red) rather than firing on every poll.

This is the file to edit if you want to change how a "Healthy" vs "Degraded"
vs "Critical" score is calculated (`scoreToHealth()`).

### `internal/notifier` — email alerts
Sends an email (via SMTP, configured in `config.yaml`) when a product's
`HealthLevel` changes, gated by `on_state_change_only` so it doesn't spam on
every 30-second poll. Template lives in `notifier.go` (`emailTemplate`).

### `internal/auth` — login, sessions, roles
Self-contained credential store + signed-cookie session system. See §4 below
— this is dense enough to deserve its own section.

### `internal/api` — HTTP server, routing, poll loop
`Server.Start()`:
1. Runs one poll immediately, then launches `pollLoop()` as a background
   goroutine (re-polls every `poll_interval`).
2. Registers all HTTP routes on a `http.ServeMux` (see the table in §3).
3. Wraps the whole mux in `auth.Middleware` and starts `http.ListenAndServe`.

`poll()` calls the collector (real or mock), feeds the result to the
aggregator, stores the resulting `Summary` for `/api/summary` to serve, and
hands state-change events to the notifier.

### `web/index.html` + `web/login.html` — the frontend
Static HTML/CSS/vanilla-JS, no build step, no framework. Served via
`http.ServeFile` directly from disk on every request — **edit the `.html`
files and refresh the browser; no rebuild or restart needed** (this is *not*
true for `.go` files, which are compiled — see §5).

`index.html`'s JS polls `/api/summary` every `poll_interval`, diffs the new
snapshot against the last one to derive alert-ticker entries client-side
(no server-side event log exists), and re-renders the namespace grid sorted
"issues first" (`rankHealth`). The drill-down modal and export menu are
gated to `currentRole === 'admin'` (set from `/api/me`).

---

## 3. HTTP routes

All routes are registered in `internal/api/server.go:Start()`:

| Route | Handler | Auth |
|---|---|---|
| `GET/POST /login` | `auth.HandleLogin` | public (passes through `auth.Middleware`) |
| `GET /logout` | `auth.HandleLogout` | public |
| `GET /api/summary` | `handleSummary` | any authenticated session |
| `GET /api/mode` | `handleMode` | any authenticated session |
| `GET /api/me` | `handleMe` | any authenticated session |
| `GET /api/export` | `handleExport` | **admin only** — wrapped in `auth.RequireAdmin` |
| `GET /favicon.svg` | inline handler | public |
| `GET /*` (everything else) | `handleIndex` | any authenticated session |

Every route except `/login`/`/logout` passes through `auth.Middleware`, which
redirects unauthenticated requests to `/login`. `/api/export` additionally
requires the `admin` role server-side; everything else that *looks* admin-only
in the UI (the drill-down modal) is gated client-side only — see §4.2 for why
that's currently safe but worth knowing.

---

## 4. The auth model in detail

### 4.1 How a session works (no database, no JWT library)
This is a hand-rolled, stateless, signed-cookie scheme — not OAuth/OIDC, not a
standard JWT library, just HMAC-SHA256 over a custom payload:

1. **Login**: `HandleLogin` checks the submitted username/password against an
   in-memory `[]User` list (built at startup from `ADMIN_USER`/`ADMIN_PASS`/
   `VIEWER_USER`/`VIEWER_PASS` env vars, defaulting to `admin`/`admin` and
   `viewer`/`viewer` if unset — see the warnings this prints at boot).
2. **Token**: on success, `createToken(username, role)` builds
   `base64url(username + "\x00" + role + "\x00" + expiry)`, computes an
   HMAC-SHA256 signature over it using `DASHBOARD_SECRET`, and concatenates
   them as `payload.signature_hex`. This becomes the `k8s_session` cookie
   value (`HttpOnly`, `SameSite=Lax`, 24-hour expiry).
3. **Every subsequent request**: `auth.Middleware` calls `parseToken`, which
   re-computes the HMAC over the payload and compares it
   (constant-time, via `hmac.Equal`) against the signature in the cookie. If
   it matches and hasn't expired, the request proceeds with `*Claims{Username,
   Role}` injected into the request context — retrievable anywhere downstream
   via `auth.ClaimsFromContext(r.Context())`.

No session store, no database, no server-side state at all — any replica with
the same `DASHBOARD_SECRET` can validate any session's cookie. This is *why*
`DASHBOARD_SECRET` must be both **set** and **shared** across replicas (see
[`docs/PRODUCTION_READINESS.md`](PRODUCTION_READINESS.md) §1.1) — without a
fixed shared secret, sessions break on restart and differ per-replica.

### 4.2 Admin vs. Viewer — what actually differs
Both roles see **identical data** — `/api/summary` returns the same JSON
regardless of role; nothing is filtered server-side by role. The only
differences are in *capability*:

| Capability | Viewer | Admin |
|---|---|---|
| View namespace health, stats, gauge, ticker | ✅ | ✅ |
| Open the drill-down modal (per-pod detail) | ❌ — hidden client-side (`currentRole !== 'admin'` early-return in `index.html`) | ✅ |
| Use the export menu (JSON/CSV download) | ❌ — hidden client-side **and** the server returns `403 forbidden: admin role required` (`auth.RequireAdmin` wraps `/api/export`) | ✅ |

In short: **`/api/export` is the only route enforced server-side**; the modal
is a client-side convenience gate. That's safe today because no data exposed
through the modal is more sensitive than what `/api/summary` already returns
to everyone — but it's a pattern to be deliberate about (see
[`docs/PRODUCTION_READINESS.md`](PRODUCTION_READINESS.md) §2.2) if you ever
add an admin-only *write* action.

### 4.3 If you swap in Keycloak / OIDC later
The clean integration point is `HandleLogin` — replace its credential check
with an OIDC Authorization Code flow against Keycloak (using `go-oidc` +
`oauth2`), map the Keycloak realm role (e.g. `dashboard-admin`) to
`auth.RoleAdmin`/`auth.RoleViewer`, and call the *existing* `createToken` to
mint the same `k8s_session` cookie. Everything downstream — `Middleware`,
`RequireAdmin`, `/api/me`, the frontend role gating — stays untouched, because
the session/cookie layer is decoupled from how the identity was established.

---

## 5. Local development & the "live HTML, compiled Go" distinction

This trips people up, so it's worth calling out explicitly:

- **`web/*.html`** are served via `http.ServeFile` reading straight from disk
  on every request. Edit them, refresh your browser — done. No restart.
- **`internal/**/*.go` and `cmd/**/*.go`** are compiled into the binary at
  `go build`/`go run` time. Editing them has **no effect** on an already-running
  server — you must stop the old process and `go run ./cmd/server -mock
  -config config/config.yaml` again to pick up the change.

Run it locally with:
```bash
go run ./cmd/server -mock -config config/config.yaml
```
`-mock` forces fake data (no cluster needed); omit it to auto-detect a real
cluster via `~/.kube/config` (falls back to mock automatically if none is
found). See the root [`README.md`](../README.md) for the Docker-based path.

---

## 6. Deployment topology (CI/CD)

```
   git push                 Jenkins pipeline                  ArgoCD                K8s
 ──────────────▶  ┌──────────────────────────────┐   ┌──────────────────┐   ┌─────────────┐
                  │ build → SonarQube → dep-check │   │ watches the      │   │ k8s-dashboard│
                  │ → Trivy (image) → build+push  │──▶│ manifest repo,   │──▶│ namespace,   │
                  │ → sign image → SBOM → DAST    │   │ auto-syncs +     │   │ per-env      │
                  │ → (prod: human approval gate) │   │ self-heals       │   │ Applications │
                  └──────────────────────────────┘   └──────────────────┘   └─────────────┘
```

- **`Jenkinsfile`** (repo root) — the full pipeline: builds the Go binary,
  runs SonarQube + dependency-check + Docker image vulnerability scanning,
  builds/signs/pushes the image (tagged with the build number on
  dev/staging/UAT, and with the contents of the `VERSION` file on the `prod`
  branch), generates an SBOM, runs DAST, and — only on the `prod` branch —
  gates the release behind a **human approval stage** before updating the
  deployment manifest.
- **`k8s/argocd/*.yaml`** — one ArgoCD `Application` per environment (`dev`,
  `staging`, `uat`, `prod`), each pointing at a different branch of a separate
  manifest repo (`kubernetes-manifest/k8s-dashboard-manifest.git`) and
  namespace. ArgoCD watches that repo and auto-syncs (`prune: true, selfHeal:
  true`) — Jenkins's job ends at "update the manifest repo"; ArgoCD takes it
  from there into the cluster. The `prod` Application also wires up email
  notifications on sync success/failure, health-status changes, and
  crash-loop detection.
- **`k8s/k8s/*.yaml`** — the actual Kubernetes manifests (Namespace,
  ConfigMap, Deployment, Service, HTTPRoute, ServiceAccount, ClusterRole,
  ClusterRoleBinding) that ArgoCD applies. Numbered to show apply order.
- **`docker-compose.yml`** / **`Dockerfile`** — local/dev convenience path
  (`docker compose up` for mock mode, `--profile real` to mount your
  kubeconfig) — not used in the CI/CD path above, which builds its own image.

For the gaps in this pipeline worth closing before relying on it in
production (image scanning thresholds, RBAC review, etc.), see
[`docs/PRODUCTION_READINESS.md`](PRODUCTION_READINESS.md) Phase 3.

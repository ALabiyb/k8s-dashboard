# K8s Platform Health Dashboard

A lightweight Go dashboard that watches your Kubernetes namespaces and shows a
per-product health score — green/amber/red — with email alerts on state changes
and Keycloak SSO for single sign-on.

```
Product: ecommerce    ██████████ 12/12  ● Healthy
Product: analytics    █████░░░░░  7/12  ● Critical  → email sent
Product: auth         ████████░░  8/10  ● Degraded
```

## Quick start (mock mode — no cluster needed)

```bash
docker compose up
```

Open **http://localhost:8080** — no Go, no Kubernetes cluster needed. Runs with
fake data and shows a yellow "Mock mode" banner so you know it isn't real.

## Real cluster mode

```bash
docker compose --profile real up   # mounts ~/.kube/config automatically
```

## With Keycloak SSO

```bash
OIDC_CLIENT_SECRET=<your-client-secret> docker compose up
```

> **Note:** The `redirect_url` in `config/config.yaml` must match a registered
> Valid Redirect URI in your Keycloak client. For docker compose the URL is
> `http://localhost:8080/auth/callback`. For local `go run` it is
> `http://localhost:8090/auth/callback`.
>
> Your Keycloak user must have the realm role `dashboard-admin` or
> `dashboard-viewer` assigned.

## Local development

```powershell
# With mock data (no cluster needed):
$env:OIDC_CLIENT_SECRET = "your-secret"; go run ./cmd/server -mock -config config/config.yaml

# With a real kubeconfig:
$env:KUBECONFIG = "C:\Users\you\.kube\config"
$env:OIDC_CLIENT_SECRET = "your-secret"
go run ./cmd/server -config config/config.yaml
```

Open **http://localhost:8090**

## Default credentials (local / non-SSO)

| Role   | Username | Password | Access          |
|--------|----------|----------|-----------------|
| Admin  | admin    | admin    | Full + export   |
| Viewer | viewer   | viewer   | Read-only       |

Override with `ADMIN_PASS` / `VIEWER_PASS` env vars before exposing externally.

## Documentation

📖 **[Architecture, technology & operations guide](docs/ARCHITECTURE.md)** —
tech stack, architecture diagrams, component breakdown, auth/session model,
RBAC, all run/deploy instructions, and the CI/CD → ArgoCD pipeline. **Start here.**

🚦 **[Production readiness checklist](docs/PRODUCTION_READINESS.md)** — ordered
list of what to harden before exposing to real users (secrets, TLS, audit
logging, RBAC review, and more).

## TV Wall Display — `/tv` kiosk mode (recommended)

The `/tv` endpoint serves the dashboard with **no authentication required** —
no login form, no cookie, no token. The TV iframe just hits
`http://<host>/tv/` and the dashboard renders with public read-only data.

### How it works

```
iframe → GET /tv/                       ← serves the SPA (HTML + JS)
       → GET /tv/me        → {"role":"viewer","username":"tv"}
       → GET /tv/mode      → {"mock":false}
       → GET /tv/summary   → cluster health JSON
```

The SPA is the same `index.html` used for the authenticated dashboard. The
JS detects `window.location.pathname.startsWith('/tv')` at load time and
swaps every `fetch('/api/...')` for `fetch('/tv/...')`. The `/tv/*` routes
are exempt from the auth middleware (see `auth.Middleware` bypass list in
`internal/auth/auth.go`) and only ever return safe, read-only data.

### Why `/tv` and not `/embed?token=...`

The original embed-token approach set a `SameSite=Lax` session cookie. Modern
browsers block third-party cookies in cross-port iframes by default — so
after the initial load, subsequent `/api/summary` polls lost the cookie,
hit a `302 → /login`, and the JSON parser failed on the HTML response
(`Unexpected token '<', '<!DOCTYPE'... is not valid JSON`).

`/tv` avoids all of this: no cookie at all. No login redirect possible. Works
identically across Chrome, Firefox, Safari, and any TV kiosk browser.

### Security stance

- The TV server (`192.168.200.78`) is on the internal LAN — not exposed to
  the internet.
- `/tv/*` endpoints only return health data (namespace names, pod counts,
  health status). No secrets, no node IPs, no credentials.
- No POST endpoints exist under `/tv/` — read-only by construction.
- If you ever expose this externally, gate `/tv/*` by source IP at the
  Gateway/Ingress layer.

### Setting up the TV iframe

```html
<iframe src="http://192.168.200.78:9095/tv/"></iframe>
```

That's it. No token, no secret to rotate, no Keycloak client to configure.

---

## TV Wall Display — Embed Token (deprecated)

> **Deprecated:** The `/embed?token=…` endpoint still works for backwards
> compatibility but is no longer used by the SoftNet TV wall. Prefer the
> `/tv` kiosk mode above — it's simpler, has no cookie issues in iframes,
> and requires no token management.

The `/embed` endpoint lets a kiosk (e.g. a Samsung TV) load the dashboard in
an iframe without a username/password. A static secret token is validated
server-side; the browser receives a read-only `viewer` session cookie. Normal
logins (password and Keycloak SSO) are completely unchanged.

### How it works

```
iframe → GET /embed?token=<EMBED_TOKEN>
            ↓  token validated (constant-time HMAC compare)
            ↓  Set-Cookie: k8s_session  (role=viewer, 24 h)
            ↓  302 → /
iframe → GET /  (cookie sent — same LAN IP, SameSite=Lax allows different port)
            ↓  dashboard loads in read-only viewer mode
```

The token lives in the `dashboard-secrets` Kubernetes Secret as `EMBED_TOKEN`.
**Never commit it to git.**

### Step 1 — Generate a token (on the server)

```bash
openssl rand -hex 32
```

### Step 2 — Patch it into the Secret

```bash
kubectl -n k8s-dashboard patch secret dashboard-secrets \
  --type=merge \
  -p "{\"stringData\":{\"EMBED_TOKEN\":\"$(openssl rand -hex 32)\"}}"
```

### Step 3 — Read the token back (to paste into tv.html)

```bash
kubectl -n k8s-dashboard get secret dashboard-secrets \
  -o jsonpath='{.data.EMBED_TOKEN}' | base64 -d && echo
```

### Step 4 — Restart the deployment

```bash
kubectl -n k8s-dashboard rollout restart deployment/k8s-dashboard
```

### Step 5 — Set the iframe URL in tv.html

Use the **HTTP NodePort** to avoid the self-signed TLS certificate error on the
Samsung TV browser:

```html
<iframe src="http://192.168.200.15:31290/embed?token=<your-token>"></iframe>
```

### Cookie behaviour by protocol

| Access path | SameSite | Secure | When to use |
|---|---|---|---|
| HTTP NodePort `:31290` | `Lax` | No | Samsung TV kiosk (same LAN IP) |
| HTTPS Istio Gateway | `None` | Yes | Cross-domain iframe over TLS |

The `/embed` handler detects the protocol via `X-Forwarded-Proto` and sets the
cookie attributes automatically — no code change needed when switching between
the two paths.

### Rotating the token

If you suspect the token was leaked:

```bash
kubectl -n k8s-dashboard patch secret dashboard-secrets \
  --type=merge \
  -p "{\"stringData\":{\"EMBED_TOKEN\":\"$(openssl rand -hex 32)\"}}"
kubectl -n k8s-dashboard rollout restart deployment/k8s-dashboard
```

Update `tv.html` with the new token and push — the old token stops working
as soon as the pod restarts.

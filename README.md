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

## TV Wall Display — Embed Token

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

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

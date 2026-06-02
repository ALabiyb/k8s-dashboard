# K8s Platform Health Dashboard

A lightweight Go dashboard that watches your Kubernetes namespaces and shows a
per-product health score — green/amber/red — with email alerts on state changes.

```
Product: ecommerce    ██████████ 12/12  ● Healthy
Product: analytics    █████░░░░░  7/12  ● Critical  → email sent
Product: auth         ████████░░  8/10  ● Degraded
```

---

## Project structure

```
k8s-dashboard/
├── cmd/server/main.go          ← entrypoint
├── internal/
│   ├── collector/              ← talks to k8s API, fetches raw state
│   ├── aggregator/             ← computes health scores
│   ├── notifier/               ← sends email alerts
│   └── api/                    ← HTTP server + poll loop
├── web/index.html              ← frontend dashboard
├── config/config.yaml          ← your settings (thresholds, SMTP, etc.)
├── k8s/
│   ├── rbac.yaml               ← ServiceAccount + ClusterRole
│   └── deployment.yaml         ← Namespace, ConfigMap, Deployment, Service
└── Dockerfile
```

---

## Option A — Docker (no Go install needed)

### Just want to see the UI with fake data?

```bash
docker compose up
```
Then open http://localhost:8080 — that's it. No Go, no k8s cluster needed.
A yellow "Mock mode" banner appears in the UI so you know it's fake data.

### Want to point it at your real cluster?

```bash
docker compose --profile real up
```
This mounts your `~/.kube/config` into the container automatically.

---

## Option B — Run with Go (faster for development)

### Prerequisites
- Go 1.21+

```bash
# 1. Install dependencies
go mod tidy

# 2. Mock mode (no cluster needed):
go run ./cmd/server -mock

# 3. Or real mode (uses ~/.kube/config automatically):
go run ./cmd/server

# 4. Open browser
open http://localhost:8080
```

---

## Deploying to your k8s cluster

### 1. Build and push the Docker image

```bash
# Replace with your actual registry
docker build -t yourregistry/k8s-dashboard:latest .
docker push yourregistry/k8s-dashboard:latest
```

### 2. Update the image in deployment.yaml

Edit `k8s/deployment.yaml` and replace:
```yaml
image: yourregistry/k8s-dashboard:latest
```

### 3. Update SMTP config in deployment.yaml

The ConfigMap in `k8s/deployment.yaml` contains `config.yaml` inline.
Update the SMTP credentials there (or use a Secret — see tip below).

### 4. Apply to the cluster

```bash
# Create RBAC first (needs cluster-admin to apply)
kubectl apply -f k8s/rbac.yaml

# Then deploy
kubectl apply -f k8s/deployment.yaml

# Check it started
kubectl -n k8s-dashboard get pods
kubectl -n k8s-dashboard logs -f deploy/k8s-dashboard
```

### 5. Access the dashboard

```bash
# Port-forward for quick access
kubectl -n k8s-dashboard port-forward svc/k8s-dashboard 8080:80
open http://localhost:8080
```

Or uncomment the Ingress block in `deployment.yaml` for permanent external access.

---

## Tweaking things

| What to change | Where |
|---|---|
| Poll interval | `config.yaml` → `server.poll_interval` |
| Health thresholds | `config.yaml` → `thresholds.healthy / degraded` |
| SMTP settings | `config.yaml` → `notifications.email` |
| Excluded namespaces | `config.yaml` → `excluded_namespaces` |
| Health scoring logic | `internal/aggregator/aggregator.go` → `scoreToHealth()` |
| Pod crash detection | `internal/collector/collector.go` → `enrichWithPodProblems()` |
| Email template | `internal/notifier/notifier.go` → `emailTemplate` |
| Dashboard UI | `web/index.html` |

---

## Security tip: store SMTP password in a k8s Secret

Instead of putting the password in the ConfigMap, create a Secret:

```bash
kubectl -n k8s-dashboard create secret generic smtp-credentials \
  --from-literal=password=your-actual-password
```

Then mount it as an env variable in the Deployment and read it in the config loader.
(Left as an exercise — see `config/config.go` to add env var override support.)

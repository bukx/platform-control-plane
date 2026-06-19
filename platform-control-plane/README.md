# Platform Control Plane

`platform-control-plane` is a principal-level Go platform engineering showcase repo: a thin internal developer platform control plane that exposes environment classes, accepts environment requests, enforces approval policy, persists state in Postgres, emits OpenTelemetry telemetry, reconciles requests through async workers, and publishes real GitOps commits plus Kubernetes bootstrap artifacts.

This is intentionally positioned above a single microservice demo. It reads like the beginning of a real internal platform:

- API server with health, readiness, and versioned endpoints
- CLI for platform operators and app teams
- Domain, service, store, and transport layers separated cleanly
- Policy-aware approval flow for production-grade environment classes
- Structured logging, graceful shutdown, tests, Docker, and CI
- Selectable memory or Postgres persistence
- OpenTelemetry tracing plus Prometheus-scrapable metrics
- Async worker queue for reconciliation
- Header-based auth/RBAC with signed approvals
- GitOps reconcile output that renders Argo CD and Kubernetes manifests, then commits them into Git

## Why This Repo Works For Platform Engineering

Hiring managers and staff/principal reviewers usually want more than "here is a CRUD app in Go." This repo shows platform thinking:

- clear control-plane boundaries
- policy enforcement before provisioning
- idempotent reconcile workflow
- operator-facing CLI
- production-ish project layout
- docs that explain architecture instead of only usage

## Architecture

```text
platform-control-plane/
├── cmd/
│   ├── platformd/      # HTTP API server
│   └── platformctl/    # Operator/app-team CLI
├── docs/               # Architecture and extension notes
├── examples/           # Sample API payloads
├── internal/
│   ├── api/            # HTTP handlers and routing
│   ├── config/         # Environment-based config
│   ├── domain/         # Core types and state transitions
│   ├── observability/  # Structured logging + OpenTelemetry bootstrap
│   ├── reconcile/      # GitOps and Kubernetes reconcile adapters
│   ├── service/        # Policy and workflow logic
│   └── store/          # Memory and Postgres repository implementations
├── .github/workflows/ci.yml
├── compose.yaml
├── Dockerfile
└── Makefile
```

## Resource Model

The control plane currently manages two resources:

- `EnvironmentClass`: the golden-path template a team can request, such as `preview`, `shared-staging`, or `production`
- `EnvironmentRequest`: a request for runtime capacity tied to an application, team, region, TTL, and application repository

## Default Environment Classes

- `preview`: fast, self-service environments for branch or PR validation
- `shared-staging`: shared integration space with longer TTL
- `production`: approval-gated class intended to show policy separation

## Quick Start

```bash
make dev-stack-up

export PLATFORM_STORAGE_BACKEND=postgres
export PLATFORM_POSTGRES_DSN=postgres://platform:platform@localhost:5432/platform?sslmode=disable
export PLATFORM_OTLP_ENDPOINT=localhost:4318
export PLATFORM_GITOPS_REPO_PATH=$PWD/state/gitops
export PLATFORM_APPROVAL_HMAC_SECRET=dev-approval-secret
export PLATFORM_JWT_HS256_SECRET=dev-jwt-secret

make run
```

In another terminal:

```bash
make cli-classes
make cli-request-create
make cli-request-list
```

Or use `curl` directly:

```bash
curl -s http://localhost:8080/v1/environment-classes | jq
curl -s -X POST http://localhost:8080/v1/environment-requests \
  -H 'Content-Type: application/json' \
  --data @examples/request-preview.json | jq
```

## Example Flow

Create a preview environment request:

```bash
REQUESTER_TOKEN=$(go run ./cmd/platformctl token mint \
  --subject app-team \
  --actor app-team@example.com \
  --role requester \
  --secret "$PLATFORM_JWT_HS256_SECRET")

go run ./cmd/platformctl request create \
  --api http://localhost:8080 \
  --app payments-api \
  --team platform \
  --class preview \
  --region us-east-1 \
  --ttl 24 \
  --owner mcmoney \
  --repo https://github.com/mcmoney/payments-api \
  --revision main \
  --token "$REQUESTER_TOKEN"
```

Create a production request, approve it, then reconcile it:

```bash
REQUESTER_TOKEN=$(go run ./cmd/platformctl token mint \
  --subject app-team \
  --actor app-team@example.com \
  --role requester \
  --secret "$PLATFORM_JWT_HS256_SECRET")

APPROVER_TOKEN=$(go run ./cmd/platformctl token mint \
  --subject platform-approver \
  --actor platform-approver@example.com \
  --role approver \
  --secret "$PLATFORM_JWT_HS256_SECRET")

ADMIN_TOKEN=$(go run ./cmd/platformctl token mint \
  --subject platform-operator \
  --actor platform-operator@example.com \
  --role admin \
  --secret "$PLATFORM_JWT_HS256_SECRET")

VIEWER_TOKEN=$(go run ./cmd/platformctl token mint \
  --subject platform-auditor \
  --actor platform-auditor@example.com \
  --role viewer \
  --secret "$PLATFORM_JWT_HS256_SECRET")

REQ_ID=$(go run ./cmd/platformctl request create \
  --api http://localhost:8080 \
  --app billing-api \
  --team platform \
  --class production \
  --region us-east-1 \
  --ttl 24 \
  --owner mcmoney \
  --repo https://github.com/mcmoney/billing-api \
  --revision main \
  --token "$REQUESTER_TOKEN" | jq -r .id)

go run ./cmd/platformctl request approve \
  --api http://localhost:8080 \
  --id "$REQ_ID" \
  --token "$APPROVER_TOKEN" \
  --approval-secret "$PLATFORM_APPROVAL_HMAC_SECRET"

go run ./cmd/platformctl request reconcile \
  --api http://localhost:8080 \
  --id "$REQ_ID" \
  --token "$ADMIN_TOKEN"

go run ./cmd/platformctl request wait \
  --api http://localhost:8080 \
  --id "$REQ_ID" \
  --token "$VIEWER_TOKEN"
```

After reconciliation you will find generated manifests under `state/gitops/clusters/<region>/teams/<team>/<app>/<request-id>/`:

- `namespace.yaml`
- `resourcequota.yaml`
- `networkpolicy.yaml`
- `argocd-application.yaml`

If `PLATFORM_K8S_APPLY=true` and a kubeconfig is available, the reconciler also bootstraps the namespace, quota, and default-deny ingress policy directly into the target cluster.

If `PLATFORM_GIT_COMMIT_ENABLED=true`, every reconcile also stages and commits the generated GitOps manifests inside `PLATFORM_GITOPS_REPO_PATH`. If `PLATFORM_GIT_PUSH_ENABLED=true`, it also pushes `HEAD` to `PLATFORM_GIT_REMOTE:PLATFORM_GIT_BRANCH`.

## Observability

- traces: OTLP/HTTP via `PLATFORM_OTLP_ENDPOINT`
- metrics: `GET /metrics` in Prometheus format
- logs: structured JSON via `log/slog`

Local Jaeger is available at `http://localhost:16686` after `make dev-stack-up`.

## Storage

- `PLATFORM_STORAGE_BACKEND=memory` for quick demos
- `PLATFORM_STORAGE_BACKEND=postgres` for persistent state

The Postgres repository auto-creates its tables at startup and seeds the default environment classes.

## Auth And Approval

API auth is bearer-token based now.

- local/dev mode: HS256 JWTs via `PLATFORM_JWT_HS256_SECRET`
- production mode: OIDC discovery/JWKS via `PLATFORM_OIDC_ISSUER_URL` and `PLATFORM_OIDC_AUDIENCE`

Supported roles from the configured token role claim:

- `viewer`
- `requester`
- `approver`
- `admin`

Relevant auth env vars:

- `PLATFORM_OIDC_ISSUER_URL`
- `PLATFORM_OIDC_AUDIENCE`
- `PLATFORM_OIDC_ROLES_CLAIM`
- `PLATFORM_OIDC_SUBJECT_CLAIM`
- `PLATFORM_OIDC_ACTOR_CLAIM`
- `PLATFORM_JWT_HS256_SECRET`

Production-style approvals are still HMAC-signed using `PLATFORM_APPROVAL_HMAC_SECRET`. The CLI computes the approval signature automatically when you pass `--approval-secret` or set the env var.

## Async Reconciliation

`POST /v1/environment-requests/{id}/reconcile` now enqueues work and returns `202 Accepted`.

Background workers are controlled with:

- `PLATFORM_RECONCILE_WORKERS`
- `PLATFORM_QUEUE_BUFFER`
- `PLATFORM_RECONCILE_MAX_ATTEMPTS`
- `PLATFORM_RECONCILE_LEASE_SECONDS`
- `PLATFORM_RECONCILE_POLL_MS`
- `PLATFORM_RECONCILE_BACKOFF_SECONDS`
- `PLATFORM_RECONCILE_MAX_BACKOFF_SECONDS`

The request lifecycle is now:

`pending_approval -> approved -> queued -> reconciling -> ready`

When `PLATFORM_STORAGE_BACKEND=postgres`, the queue is durable and backed by a `reconcile_jobs` table with retry/backoff and lease recovery semantics.

## Next Principal-Level Extensions

- Add RBAC and signed approvals
- Add queue-backed asynchronous reconcilers
- Introduce workload templates, quotas, and cost controls

The repo is intentionally shaped so those additions fit naturally instead of requiring a rewrite.

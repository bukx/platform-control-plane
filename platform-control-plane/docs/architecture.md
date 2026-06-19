# Architecture Notes

## Intent

This project models a small internal developer platform control plane, not a generic CRUD API.

The important thing is the workflow:

1. A team chooses a sanctioned `EnvironmentClass`
2. The control plane validates the request against class policy
3. Sensitive classes require approval
4. Approved requests are queued for asynchronous reconciliation
5. Workers write GitOps artifacts, commit them into Git, optionally push upstream, and can bootstrap base Kubernetes resources

That sequence maps cleanly to real platform work, where the control plane owns policy and orchestration while downstream systems own provisioning.

## Layering

### `internal/domain`

Pure business types and request states. This is where the resource model lives.

### `internal/store`

Persistence boundary. The repo supports both in-memory and Postgres implementations so local demos stay easy while the production story remains credible.

### `internal/service`

Policy engine and workflow logic:

- validates environment class constraints
- assigns initial status
- enforces approval-before-reconcile
- verifies signed approvals
- enqueues reconciliation work instead of doing it inline
- records telemetry counters and spans
- updates timestamps for operational visibility

### `internal/auth`

Simple platform-facing auth context:

- actor identity from headers
- RBAC role checks at the API boundary
- HMAC-signed approval verification for sensitive changes

### `internal/queue`

Background worker pool for reconciliation jobs. This keeps the API responsive and makes the control plane read more like a real operator-oriented system than a synchronous demo handler.

### `internal/reconcile`

Turns an approved request into something an actual platform team would recognize:

- Argo CD `Application`
- Kubernetes `Namespace`
- `ResourceQuota`
- default-deny ingress `NetworkPolicy`

Those manifests are written into a GitOps tree, committed into Git, optionally pushed to a remote, and the same reconcile can optionally apply the namespace bootstrap directly to a live cluster.

### `internal/api`

HTTP transport only. Handlers decode requests, call the service layer, and return JSON.

### `cmd/platformd`

Process bootstrap: config, logger, graceful shutdown, HTTP server lifecycle.

### `cmd/platformctl`

Thin CLI that makes the API usable in demos, scripts, and GitHub Actions.

## Why This Feels Production-Shaped

- Clear control plane versus data plane separation
- Policy modeled as a first-class concern
- Explicit states instead of hidden side effects
- Operability basics built in from day one
- Persistence and telemetry are first-class instead of afterthoughts
- Future integrations can attach to the service boundary without reworking transport

## Recommended Roadmap

1. Add persistent storage and migration tooling
2. Add audit events for approvals and reconciles
3. Add async workers and retries
4. Add authN/authZ with workload and human identities
5. Add GitOps or infra reconciler adapters

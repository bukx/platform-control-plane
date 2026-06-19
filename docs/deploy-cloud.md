# Cloud Deployment Overview

This repo is now shaped for production-style deployment, but it still expects you to bring a few real platform dependencies:

- a container image registry
- managed Postgres
- an OIDC provider
- a GitOps repository and credentials
- Kubernetes secrets sourced from your cloud secret manager

The Helm chart lives in `charts/platform-control-plane/` and the environment-specific values overlays live in `deploy/kubernetes/production/`.

## Baseline Production Flow

1. Build and push the image for `platformd` and `platformmigrate`.
2. Create the Kubernetes namespace:

```bash
kubectl apply -f deploy/kubernetes/production/namespace.yaml
```

3. Sync secrets from your cloud secret manager into a Kubernetes secret named `platform-control-plane-secrets`.
   Include `GH_TOKEN` if you enable pull-request promotion creation.
4. Run the Helm release with the environment-specific values file:

```bash
helm upgrade --install platform-control-plane charts/platform-control-plane \
  --namespace platform-system \
  -f deploy/kubernetes/production/values-aws.yaml
```

5. Confirm the pre-install migration job succeeds.
6. Confirm `/readyz` returns `200` only after Postgres, auth, queue, and GitOps checks pass.

## Production Behavior Added In This Pass

- strict production config validation via `PLATFORM_STRICT_PRODUCTION`
- explicit `platformmigrate` binary for release-safe schema application
- real `/readyz` checks for repository, queue backend, GitOps path, Kubernetes dependency, and OIDC discovery
- hardened HTTP server timeouts
- secret-manager-friendly config via `*_FILE` support and secret-backed env vars in Helm
- promotion-branch and GitHub PR flow for GitOps rollout instead of direct branch-only updates
- quota profile, policy pack, and estimated monthly cost metadata per environment class

## Image Build

Example build:

```bash
docker build -t ghcr.io/bukx/platform-control-plane:v0.3.0 .
docker push ghcr.io/bukx/platform-control-plane:v0.3.0
```

Update the values file for your registry before deploying.

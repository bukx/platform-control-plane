# Deploy On GCP

Recommended stack:

- GKE
- Cloud SQL for Postgres
- Secret Manager
- GKE Ingress or Gateway

## Suggested Wiring

- Mirror your runtime secrets into `platform-control-plane-secrets` with External Secrets or Config Connector.
- Use `deploy/kubernetes/production/values-gcp.yaml` as the starting point.
- Use Workload Identity if the control plane eventually needs GCP API access.
- Use a private Cloud SQL connection path or authorized network configuration for Postgres access.
- If PR creation is enabled, sync `GH_TOKEN` into the release secret.

## Deploy

```bash
helm upgrade --install platform-control-plane charts/platform-control-plane \
  --namespace platform-system \
  -f deploy/kubernetes/production/values-gcp.yaml
```

## GCP Notes

- Artifact Registry is the natural image home.
- If you keep GitOps state on disk in-cluster, use a standard PVC or Filestore-backed class depending on your durability needs.

# Deploy On Azure

Recommended stack:

- AKS
- Azure Database for PostgreSQL
- Azure Key Vault
- Application Routing / NGINX ingress

## Suggested Wiring

- Sync runtime secrets from Key Vault into `platform-control-plane-secrets` with the Secrets Store CSI Driver or External Secrets.
- Use `deploy/kubernetes/production/values-azure.yaml` as the starting point.
- Configure OIDC against Microsoft Entra ID or your enterprise IdP.

## Deploy

```bash
helm upgrade --install platform-control-plane charts/platform-control-plane \
  --namespace platform-system \
  -f deploy/kubernetes/production/values-azure.yaml
```

## Azure Notes

- ACR is the simplest image target.
- Use a managed identity if future reconcile steps need Azure API access.
- Prefer a production storage class for the GitOps PVC if manifests must survive pod rescheduling.

# Deploy On AWS

Recommended stack:

- EKS
- RDS Postgres
- AWS Secrets Manager
- ALB Ingress Controller
- optional IRSA for Git or cloud API access

## Suggested Wiring

- Store `PLATFORM_POSTGRES_DSN`, `PLATFORM_APPROVAL_HMAC_SECRET`, `PLATFORM_OIDC_ISSUER_URL`, and `PLATFORM_OIDC_AUDIENCE` in AWS Secrets Manager.
- Sync those into the `platform-control-plane-secrets` Kubernetes secret with External Secrets Operator.
- Use `deploy/kubernetes/production/values-aws.yaml` as the starting point.
- Point OIDC at Cognito, Okta, Auth0, or your enterprise IdP.
- Use RDS with SSL-enabled DSNs for real production.

## Deploy

```bash
helm upgrade --install platform-control-plane charts/platform-control-plane \
  --namespace platform-system \
  -f deploy/kubernetes/production/values-aws.yaml
```

## AWS Notes

- Prefer ECR for the runtime image.
- Use an EBS-backed PVC if the local GitOps worktree needs persistent disk.
- If you move to PR-based GitOps promotion later, bind credentials through IRSA or a Git credential sidecar/secret.

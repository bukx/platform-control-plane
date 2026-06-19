package config

import (
	"fmt"
	"os"
	"strconv"
)

type Config struct {
	Address                string
	Env                    string
	AuthEnabled            bool
	ApprovalHMACSecret     string
	OIDCIssuerURL          string
	OIDCAudience           string
	OIDCRolesClaim         string
	OIDCSubjectClaim       string
	OIDCActorClaim         string
	JWTDevHS256Secret      string
	StorageBackend         string
	PostgresDSN            string
	GitOpsRepoPath         string
	GitOpsRepoURL          string
	GitAuthorName          string
	GitAuthorEmail         string
	GitBranch              string
	GitRemoteName          string
	GitCommitEnabled       bool
	GitPushEnabled         bool
	ClusterName            string
	KubeApply              bool
	KubeconfigPath         string
	ReconcileWorkers       int
	QueueBuffer            int
	ReconcileMaxAttempts   int
	ReconcileLeaseSec      int
	ReconcilePollMS        int
	ReconcileBackoffSec    int
	ReconcileBackoffMaxSec int
	OTLPTraceEndpoint      string
	OTelServiceName        string
	PrometheusEnabled      bool
}

func Load() (Config, error) {
	cfg := Config{
		Address:                envOrDefault("PLATFORM_ADDRESS", ":8080"),
		Env:                    envOrDefault("PLATFORM_ENV", "dev"),
		AuthEnabled:            envOrDefault("PLATFORM_AUTH_ENABLED", "true") == "true",
		ApprovalHMACSecret:     os.Getenv("PLATFORM_APPROVAL_HMAC_SECRET"),
		OIDCIssuerURL:          os.Getenv("PLATFORM_OIDC_ISSUER_URL"),
		OIDCAudience:           os.Getenv("PLATFORM_OIDC_AUDIENCE"),
		OIDCRolesClaim:         envOrDefault("PLATFORM_OIDC_ROLES_CLAIM", "role"),
		OIDCSubjectClaim:       envOrDefault("PLATFORM_OIDC_SUBJECT_CLAIM", "sub"),
		OIDCActorClaim:         envOrDefault("PLATFORM_OIDC_ACTOR_CLAIM", "email"),
		JWTDevHS256Secret:      os.Getenv("PLATFORM_JWT_HS256_SECRET"),
		StorageBackend:         envOrDefault("PLATFORM_STORAGE_BACKEND", "memory"),
		PostgresDSN:            os.Getenv("PLATFORM_POSTGRES_DSN"),
		GitOpsRepoPath:         envOrDefault("PLATFORM_GITOPS_REPO_PATH", "./state/gitops"),
		GitOpsRepoURL:          os.Getenv("PLATFORM_GITOPS_REPO_URL"),
		GitAuthorName:          envOrDefault("PLATFORM_GIT_AUTHOR_NAME", "Platform Control Plane"),
		GitAuthorEmail:         envOrDefault("PLATFORM_GIT_AUTHOR_EMAIL", "platform@example.com"),
		GitBranch:              envOrDefault("PLATFORM_GIT_BRANCH", "main"),
		GitRemoteName:          envOrDefault("PLATFORM_GIT_REMOTE", "origin"),
		GitCommitEnabled:       envOrDefault("PLATFORM_GIT_COMMIT_ENABLED", "true") == "true",
		GitPushEnabled:         envOrDefault("PLATFORM_GIT_PUSH_ENABLED", "false") == "true",
		ClusterName:            envOrDefault("PLATFORM_CLUSTER_NAME", "platform-dev"),
		KubeApply:              envOrDefault("PLATFORM_K8S_APPLY", "false") == "true",
		KubeconfigPath:         os.Getenv("PLATFORM_KUBECONFIG"),
		ReconcileWorkers:       intEnvOrDefault("PLATFORM_RECONCILE_WORKERS", 2),
		QueueBuffer:            intEnvOrDefault("PLATFORM_QUEUE_BUFFER", 32),
		ReconcileMaxAttempts:   intEnvOrDefault("PLATFORM_RECONCILE_MAX_ATTEMPTS", 5),
		ReconcileLeaseSec:      intEnvOrDefault("PLATFORM_RECONCILE_LEASE_SECONDS", 30),
		ReconcilePollMS:        intEnvOrDefault("PLATFORM_RECONCILE_POLL_MS", 750),
		ReconcileBackoffSec:    intEnvOrDefault("PLATFORM_RECONCILE_BACKOFF_SECONDS", 1),
		ReconcileBackoffMaxSec: intEnvOrDefault("PLATFORM_RECONCILE_MAX_BACKOFF_SECONDS", 30),
		OTLPTraceEndpoint:      os.Getenv("PLATFORM_OTLP_ENDPOINT"),
		OTelServiceName:        envOrDefault("PLATFORM_OTEL_SERVICE_NAME", "platform-control-plane"),
		PrometheusEnabled:      envOrDefault("PLATFORM_PROMETHEUS_ENABLED", "true") == "true",
	}

	if port := os.Getenv("PORT"); port != "" {
		if _, err := strconv.Atoi(port); err != nil {
			return Config{}, fmt.Errorf("invalid PORT value %q: %w", port, err)
		}
		cfg.Address = ":" + port
	}

	return cfg, nil
}

func envOrDefault(key, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}

	return value
}

func intEnvOrDefault(key string, fallback int) int {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

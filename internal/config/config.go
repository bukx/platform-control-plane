package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Address                string
	Env                    string
	StrictProduction       bool
	AuthEnabled            bool
	ApprovalHMACSecret     string
	OIDCIssuerURL          string
	OIDCAudience           string
	OIDCRolesClaim         string
	OIDCSubjectClaim       string
	OIDCActorClaim         string
	JWTDevHS256Secret      string
	AllowStaticJWTInProd   bool
	StorageBackend         string
	PostgresDSN            string
	AutoMigrate            bool
	GitOpsRepoPath         string
	GitOpsRepoURL          string
	GitAuthorName          string
	GitAuthorEmail         string
	GitBranch              string
	GitBaseBranch          string
	GitRemoteName          string
	GitCommitEnabled       bool
	GitPushEnabled         bool
	GitPromotionMode       string
	GitPromotionBranchPref string
	GitProvider            string
	GitHubRepo             string
	GitPRCreate            bool
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
	ReadTimeoutSec         int
	WriteTimeoutSec        int
	IdleTimeoutSec         int
	ShutdownTimeoutSec     int
	ReadinessTimeoutSec    int
}

func Load() (Config, error) {
	env := envOrDefault("PLATFORM_ENV", "dev")
	strictProduction := envOrDefault("PLATFORM_STRICT_PRODUCTION", "") == "true" || env == "prod"
	cfg := Config{
		Address:                envOrDefault("PLATFORM_ADDRESS", ":8080"),
		Env:                    env,
		StrictProduction:       strictProduction,
		AuthEnabled:            envOrDefault("PLATFORM_AUTH_ENABLED", "true") == "true",
		ApprovalHMACSecret:     secretOrEnv("PLATFORM_APPROVAL_HMAC_SECRET"),
		OIDCIssuerURL:          strings.TrimSpace(secretOrEnv("PLATFORM_OIDC_ISSUER_URL")),
		OIDCAudience:           strings.TrimSpace(secretOrEnv("PLATFORM_OIDC_AUDIENCE")),
		OIDCRolesClaim:         envOrDefault("PLATFORM_OIDC_ROLES_CLAIM", "role"),
		OIDCSubjectClaim:       envOrDefault("PLATFORM_OIDC_SUBJECT_CLAIM", "sub"),
		OIDCActorClaim:         envOrDefault("PLATFORM_OIDC_ACTOR_CLAIM", "email"),
		JWTDevHS256Secret:      secretOrEnv("PLATFORM_JWT_HS256_SECRET"),
		AllowStaticJWTInProd:   envOrDefault("PLATFORM_ALLOW_STATIC_JWT_IN_PROD", "false") == "true",
		StorageBackend:         envOrDefault("PLATFORM_STORAGE_BACKEND", "memory"),
		PostgresDSN:            secretOrEnv("PLATFORM_POSTGRES_DSN"),
		AutoMigrate:            autoMigrateDefault(env),
		GitOpsRepoPath:         envOrDefault("PLATFORM_GITOPS_REPO_PATH", "./state/gitops"),
		GitOpsRepoURL:          secretOrEnv("PLATFORM_GITOPS_REPO_URL"),
		GitAuthorName:          envOrDefault("PLATFORM_GIT_AUTHOR_NAME", "Platform Control Plane"),
		GitAuthorEmail:         envOrDefault("PLATFORM_GIT_AUTHOR_EMAIL", "platform@example.com"),
		GitBranch:              envOrDefault("PLATFORM_GIT_BRANCH", "main"),
		GitBaseBranch:          envOrDefault("PLATFORM_GIT_BASE_BRANCH", "main"),
		GitRemoteName:          envOrDefault("PLATFORM_GIT_REMOTE", "origin"),
		GitCommitEnabled:       envOrDefault("PLATFORM_GIT_COMMIT_ENABLED", "true") == "true",
		GitPushEnabled:         envOrDefault("PLATFORM_GIT_PUSH_ENABLED", "false") == "true",
		GitPromotionMode:       envOrDefault("PLATFORM_GIT_PROMOTION_MODE", "direct"),
		GitPromotionBranchPref: envOrDefault("PLATFORM_GIT_PROMOTION_BRANCH_PREFIX", "promotion"),
		GitProvider:            envOrDefault("PLATFORM_GIT_PROVIDER", "github"),
		GitHubRepo:             envOrDefault("PLATFORM_GITHUB_REPO", ""),
		GitPRCreate:            envOrDefault("PLATFORM_GIT_PR_CREATE", "false") == "true",
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
		ReadTimeoutSec:         intEnvOrDefault("PLATFORM_HTTP_READ_TIMEOUT_SECONDS", 15),
		WriteTimeoutSec:        intEnvOrDefault("PLATFORM_HTTP_WRITE_TIMEOUT_SECONDS", 30),
		IdleTimeoutSec:         intEnvOrDefault("PLATFORM_HTTP_IDLE_TIMEOUT_SECONDS", 60),
		ShutdownTimeoutSec:     intEnvOrDefault("PLATFORM_HTTP_SHUTDOWN_TIMEOUT_SECONDS", 15),
		ReadinessTimeoutSec:    intEnvOrDefault("PLATFORM_READINESS_TIMEOUT_SECONDS", 3),
	}

	if port := os.Getenv("PORT"); port != "" {
		if _, err := strconv.Atoi(port); err != nil {
			return Config{}, fmt.Errorf("invalid PORT value %q: %w", port, err)
		}
		cfg.Address = ":" + port
	}

	if err := cfg.validate(); err != nil {
		return Config{}, err
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

func secretOrEnv(key string) string {
	if filePath := strings.TrimSpace(os.Getenv(key + "_FILE")); filePath != "" {
		data, err := os.ReadFile(filePath)
		if err == nil {
			return strings.TrimSpace(string(data))
		}
	}
	return strings.TrimSpace(os.Getenv(key))
}

func autoMigrateDefault(env string) bool {
	switch strings.TrimSpace(strings.ToLower(env)) {
	case "prod", "production":
		return envOrDefault("PLATFORM_AUTO_MIGRATE", "false") == "true"
	default:
		return envOrDefault("PLATFORM_AUTO_MIGRATE", "true") == "true"
	}
}

func (c Config) validate() error {
	if c.StrictProduction {
		if !c.AuthEnabled {
			return fmt.Errorf("strict production mode requires PLATFORM_AUTH_ENABLED=true")
		}
		if c.StorageBackend != "postgres" {
			return fmt.Errorf("strict production mode requires PLATFORM_STORAGE_BACKEND=postgres")
		}
		if c.PostgresDSN == "" {
			return fmt.Errorf("strict production mode requires PLATFORM_POSTGRES_DSN")
		}
		if c.ApprovalHMACSecret == "" {
			return fmt.Errorf("strict production mode requires PLATFORM_APPROVAL_HMAC_SECRET")
		}
		if c.OIDCIssuerURL == "" {
			return fmt.Errorf("strict production mode requires PLATFORM_OIDC_ISSUER_URL")
		}
		if c.JWTDevHS256Secret != "" && !c.AllowStaticJWTInProd {
			return fmt.Errorf("strict production mode blocks PLATFORM_JWT_HS256_SECRET unless PLATFORM_ALLOW_STATIC_JWT_IN_PROD=true")
		}
	}

	if c.ReadTimeoutSec < 1 || c.WriteTimeoutSec < 1 || c.IdleTimeoutSec < 1 || c.ShutdownTimeoutSec < 1 || c.ReadinessTimeoutSec < 1 {
		return fmt.Errorf("http and readiness timeout values must be greater than zero")
	}
	if c.GitPromotionMode != "direct" && c.GitPromotionMode != "pull_request" {
		return fmt.Errorf("unsupported PLATFORM_GIT_PROMOTION_MODE %q", c.GitPromotionMode)
	}
	if c.GitPromotionMode == "pull_request" && !c.GitPushEnabled {
		return fmt.Errorf("pull-request promotion requires PLATFORM_GIT_PUSH_ENABLED=true")
	}

	return nil
}

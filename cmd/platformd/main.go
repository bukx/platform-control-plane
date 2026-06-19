package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mcmoney/platform-control-plane/internal/api"
	"github.com/mcmoney/platform-control-plane/internal/auth"
	"github.com/mcmoney/platform-control-plane/internal/config"
	"github.com/mcmoney/platform-control-plane/internal/observability"
	"github.com/mcmoney/platform-control-plane/internal/queue"
	"github.com/mcmoney/platform-control-plane/internal/reconcile"
	"github.com/mcmoney/platform-control-plane/internal/service"
	"github.com/mcmoney/platform-control-plane/internal/store"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	logger := observability.NewLogger(cfg.Env)
	authenticator, err := auth.NewAuthenticator(context.Background(), auth.Config{
		Enabled:         cfg.AuthEnabled,
		IssuerURL:       cfg.OIDCIssuerURL,
		Audience:        cfg.OIDCAudience,
		RolesClaim:      cfg.OIDCRolesClaim,
		SubjectClaim:    cfg.OIDCSubjectClaim,
		ActorClaim:      cfg.OIDCActorClaim,
		StaticJWTSecret: cfg.JWTDevHS256Secret,
	})
	if err != nil {
		log.Fatalf("setup auth: %v", err)
	}

	telemetry, err := observability.SetupTelemetry(context.Background(), observability.TelemetryConfig{
		ServiceName:       cfg.OTelServiceName,
		OTLPEndpoint:      cfg.OTLPTraceEndpoint,
		PrometheusEnabled: cfg.PrometheusEnabled,
	})
	if err != nil {
		log.Fatalf("setup telemetry: %v", err)
	}
	defer func() {
		if err := telemetry.Shutdown(context.Background()); err != nil {
			logger.Error("telemetry_shutdown_failed", "error", err)
		}
	}()

	repo, err := buildRepository(context.Background(), cfg)
	if err != nil {
		log.Fatalf("build repository: %v", err)
	}
	defer func() {
		if err := repo.Close(); err != nil {
			logger.Error("repository_close_failed", "error", err)
		}
	}()
	if err := repo.UpsertClasses(context.Background(), service.DefaultClasses()); err != nil {
		log.Fatalf("seed classes: %v", err)
	}

	reconciler, err := reconcile.NewGitOpsKubernetesReconciler(logger, reconcile.Config{
		GitOpsRepoPath: cfg.GitOpsRepoPath,
		ClusterName:    cfg.ClusterName,
		GitOpsRepoURL:  cfg.GitOpsRepoURL,
		GitAuthorName:  cfg.GitAuthorName,
		GitAuthorEmail: cfg.GitAuthorEmail,
		GitBranch:      cfg.GitBranch,
		GitRemoteName:  cfg.GitRemoteName,
		GitCommit:      cfg.GitCommitEnabled,
		GitPush:        cfg.GitPushEnabled,
		KubeApply:      cfg.KubeApply,
		KubeconfigPath: cfg.KubeconfigPath,
	})
	if err != nil {
		log.Fatalf("build reconciler: %v", err)
	}

	svc := service.New(logger, repo, reconciler, telemetry.Meter, service.Config{
		ApprovalHMACSecret: cfg.ApprovalHMACSecret,
	})
	workerCtx, workerCancel := context.WithCancel(context.Background())
	defer workerCancel()

	queueBackend, err := buildQueueBackend(repo, cfg)
	if err != nil {
		log.Fatalf("build queue backend: %v", err)
	}
	svc.SetQueue(queueBackend)
	workerPool := queue.NewWorkerPool(logger, queueBackend, svc.ProcessReconcileRequest, queue.Config{
		Workers:       cfg.ReconcileWorkers,
		LeaseDuration: time.Duration(cfg.ReconcileLeaseSec) * time.Second,
		PollInterval:  time.Duration(cfg.ReconcilePollMS) * time.Millisecond,
		BaseBackoff:   time.Duration(cfg.ReconcileBackoffSec) * time.Second,
		MaxBackoff:    time.Duration(cfg.ReconcileBackoffMaxSec) * time.Second,
	})
	workerWG := workerPool.Start(workerCtx)
	defer workerWG.Wait()

	if cfg.StorageBackend == "memory" {
		if replayed, err := svc.ReplayQueuedRequests(context.Background()); err != nil {
			logger.Error("replay_queued_requests_failed", "error", err)
		} else if replayed > 0 {
			logger.Info("replayed_queued_requests", "count", replayed)
		}
	}

	server := api.NewServer(
		logger,
		authenticator,
		svc,
		time.Duration(cfg.ReadinessTimeoutSec)*time.Second,
		api.NamedChecker("repository", repo),
		api.NamedChecker("queue", queueBackend),
		api.NamedChecker("reconciler", reconciler),
		api.NamedChecker("auth", authenticator),
	)
	handler := observability.HTTPMiddleware("platform-api", server.Handler())
	if telemetry.MetricsHandler != nil {
		root := http.NewServeMux()
		root.Handle("/metrics", telemetry.MetricsHandler)
		root.Handle("/", handler)
		handler = root
	}

	httpServer := &http.Server{
		Addr:              cfg.Address,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       time.Duration(cfg.ReadTimeoutSec) * time.Second,
		WriteTimeout:      time.Duration(cfg.WriteTimeoutSec) * time.Second,
		IdleTimeout:       time.Duration(cfg.IdleTimeoutSec) * time.Second,
	}

	go func() {
		logger.Info("platformd_starting", "address", cfg.Address, "env", cfg.Env)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("platformd_failed", "error", err)
			os.Exit(1)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.ShutdownTimeoutSec)*time.Second)
	defer cancel()

	logger.Info("platformd_shutting_down")
	workerCancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("platformd_shutdown_failed", "error", err)
		os.Exit(1)
	}

	logger.Info("platformd_stopped")
}

func buildRepository(ctx context.Context, cfg config.Config) (store.Repository, error) {
	switch cfg.StorageBackend {
	case "memory":
		return store.NewMemoryRepository(service.DefaultClasses()), nil
	case "postgres":
		if cfg.PostgresDSN == "" {
			return nil, errors.New("PLATFORM_POSTGRES_DSN is required when PLATFORM_STORAGE_BACKEND=postgres")
		}
		return store.NewPostgresRepository(ctx, cfg.PostgresDSN, store.PostgresOptions{AutoMigrate: cfg.AutoMigrate})
	default:
		return nil, errors.New("unsupported PLATFORM_STORAGE_BACKEND: " + cfg.StorageBackend)
	}
}

func buildQueueBackend(repo store.Repository, cfg config.Config) (queue.Backend, error) {
	if cfg.StorageBackend == "postgres" {
		postgresRepo, ok := repo.(*store.PostgresRepository)
		if !ok {
			return nil, errors.New("postgres storage selected but repository is not postgres")
		}
		return queue.NewPostgresBackend(postgresRepo.Pool(), cfg.ReconcileMaxAttempts), nil
	}

	return queue.NewMemoryBackend(cfg.QueueBuffer, cfg.ReconcileMaxAttempts), nil
}

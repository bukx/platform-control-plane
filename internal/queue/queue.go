package queue

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"strconv"
	"sync"
	"time"
)

var (
	ErrQueueFull = errors.New("reconcile queue is full")
	ErrNoJob     = errors.New("no reconcile job available")
)

type Processor func(context.Context, string) error

type Job struct {
	ID          int64
	RequestID   string
	Attempts    int
	MaxAttempts int
}

type Backend interface {
	Enqueue(context.Context, string) error
	Claim(context.Context, string, time.Duration) (Job, error)
	Complete(context.Context, Job) error
	Retry(context.Context, Job, error, time.Duration, bool) error
}

type Config struct {
	Workers       int
	LeaseDuration time.Duration
	PollInterval  time.Duration
	BaseBackoff   time.Duration
	MaxBackoff    time.Duration
}

type WorkerPool struct {
	logger    *slog.Logger
	backend   Backend
	processor Processor
	cfg       Config
}

func NewWorkerPool(logger *slog.Logger, backend Backend, processor Processor, cfg Config) *WorkerPool {
	if cfg.Workers < 1 {
		cfg.Workers = 1
	}
	if cfg.LeaseDuration <= 0 {
		cfg.LeaseDuration = 30 * time.Second
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 750 * time.Millisecond
	}
	if cfg.BaseBackoff <= 0 {
		cfg.BaseBackoff = 1 * time.Second
	}
	if cfg.MaxBackoff <= 0 {
		cfg.MaxBackoff = 30 * time.Second
	}

	return &WorkerPool{
		logger:    logger,
		backend:   backend,
		processor: processor,
		cfg:       cfg,
	}
}

func (w *WorkerPool) Start(ctx context.Context) *sync.WaitGroup {
	var wg sync.WaitGroup
	for i := 0; i < w.cfg.Workers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			w.runWorker(ctx, workerID)
		}(i + 1)
	}
	return &wg
}

func (w *WorkerPool) runWorker(ctx context.Context, workerID int) {
	workerName := "worker-" + time.Now().UTC().Format("150405") + "-" + strconv.Itoa(workerID)
	for {
		select {
		case <-ctx.Done():
			w.logger.Info("reconcile_worker_stopped", "worker_id", workerID)
			return
		default:
		}

		job, err := w.backend.Claim(ctx, workerName, w.cfg.LeaseDuration)
		if err != nil {
			if errors.Is(err, ErrNoJob) {
				time.Sleep(w.cfg.PollInterval)
				continue
			}
			w.logger.Error("reconcile_worker_claim_failed", "worker_id", workerID, "error", err)
			time.Sleep(w.cfg.PollInterval)
			continue
		}

		w.logger.Info("reconcile_worker_processing", "worker_id", workerID, "request_id", job.RequestID, "attempt", job.Attempts)
		if err := w.processor(ctx, job.RequestID); err != nil {
			terminal := job.Attempts >= job.MaxAttempts
			backoff := exponentialBackoff(w.cfg.BaseBackoff, w.cfg.MaxBackoff, job.Attempts)
			if retryErr := w.backend.Retry(ctx, job, err, backoff, terminal); retryErr != nil {
				w.logger.Error("reconcile_worker_retry_failed", "worker_id", workerID, "request_id", job.RequestID, "error", retryErr)
			}
			w.logger.Error("reconcile_worker_failed", "worker_id", workerID, "request_id", job.RequestID, "terminal", terminal, "error", err)
			continue
		}

		if err := w.backend.Complete(ctx, job); err != nil {
			w.logger.Error("reconcile_worker_complete_failed", "worker_id", workerID, "request_id", job.RequestID, "error", err)
		}
	}
}

func exponentialBackoff(base, max time.Duration, attempts int) time.Duration {
	if attempts < 1 {
		return base
	}
	multiplier := math.Pow(2, float64(attempts-1))
	delay := time.Duration(float64(base) * multiplier)
	if delay > max {
		return max
	}
	return delay
}

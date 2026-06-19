package queue

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresBackend struct {
	pool        *pgxpool.Pool
	maxAttempts int
}

func NewPostgresBackend(pool *pgxpool.Pool, maxAttempts int) *PostgresBackend {
	if maxAttempts < 1 {
		maxAttempts = 5
	}
	return &PostgresBackend{
		pool:        pool,
		maxAttempts: maxAttempts,
	}
}

func (p *PostgresBackend) Enqueue(ctx context.Context, requestID string) error {
	const query = `
	insert into reconcile_jobs (
		request_id, status, attempts, max_attempts, next_run_at, created_at, updated_at
	) values (
		$1, 'pending', 0, $2, now(), now(), now()
	)
	on conflict (request_id) do update set
		status = 'pending',
		attempts = 0,
		max_attempts = excluded.max_attempts,
		next_run_at = now(),
		leased_until = null,
		worker_id = '',
		last_error = '',
		updated_at = now()
	`
	_, err := p.pool.Exec(ctx, query, requestID, p.maxAttempts)
	if err != nil {
		return fmt.Errorf("enqueue reconcile job for %s: %w", requestID, err)
	}
	return nil
}

func (p *PostgresBackend) Claim(ctx context.Context, workerID string, leaseDuration time.Duration) (Job, error) {
	const query = `
	with next_job as (
		select id
		from reconcile_jobs
		where (
			status = 'pending'
			or status = 'retry'
			or (status = 'leased' and leased_until < now())
		)
		and next_run_at <= now()
		order by next_run_at, id
		for update skip locked
		limit 1
	)
	update reconcile_jobs j
	set status = 'leased',
		attempts = attempts + 1,
		worker_id = $1,
		leased_until = now() + ($2 * interval '1 second'),
		updated_at = now()
	from next_job
	where j.id = next_job.id
	returning j.id, j.request_id, j.attempts, j.max_attempts
	`

	var job Job
	err := p.pool.QueryRow(ctx, query, workerID, int(leaseDuration.Seconds())).Scan(
		&job.ID,
		&job.RequestID,
		&job.Attempts,
		&job.MaxAttempts,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Job{}, ErrNoJob
		}
		return Job{}, fmt.Errorf("claim reconcile job: %w", err)
	}
	return job, nil
}

func (p *PostgresBackend) Complete(ctx context.Context, job Job) error {
	const query = `
	update reconcile_jobs
	set status = 'done',
		leased_until = null,
		worker_id = '',
		last_error = '',
		updated_at = now()
	where id = $1
	`
	_, err := p.pool.Exec(ctx, query, job.ID)
	if err != nil {
		return fmt.Errorf("complete reconcile job %d: %w", job.ID, err)
	}
	return nil
}

func (p *PostgresBackend) Retry(ctx context.Context, job Job, cause error, backoff time.Duration, terminal bool) error {
	status := "retry"
	if terminal {
		status = "failed"
		backoff = 0
	}

	const query = `
	update reconcile_jobs
	set status = $2,
		leased_until = null,
		worker_id = '',
		last_error = $3,
		next_run_at = now() + ($4 * interval '1 second'),
		updated_at = now()
	where id = $1
	`
	_, err := p.pool.Exec(ctx, query, job.ID, status, cause.Error(), int(backoff.Seconds()))
	if err != nil {
		return fmt.Errorf("retry reconcile job %d: %w", job.ID, err)
	}
	return nil
}

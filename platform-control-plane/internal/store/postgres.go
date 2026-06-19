package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mcmoney/platform-control-plane/internal/domain"
	"github.com/mcmoney/platform-control-plane/internal/migrate"
)

type PostgresRepository struct {
	pool *pgxpool.Pool
}

type PostgresOptions struct {
	AutoMigrate bool
}

func (r *PostgresRepository) Pool() *pgxpool.Pool {
	return r.pool
}

func NewPostgresRepository(ctx context.Context, dsn string, opts PostgresOptions) (*PostgresRepository, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("create postgres pool: %w", err)
	}

	repo := &PostgresRepository{pool: pool}
	if opts.AutoMigrate {
		if err := repo.ensureSchema(ctx); err != nil {
			pool.Close()
			return nil, err
		}
	}

	return repo, nil
}

func (r *PostgresRepository) Ready(ctx context.Context) error {
	if err := r.pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping postgres: %w", err)
	}
	return nil
}

func (r *PostgresRepository) Close() error {
	r.pool.Close()
	return nil
}

func (r *PostgresRepository) UpsertClasses(ctx context.Context, classes []domain.EnvironmentClass) error {
	const query = `
	insert into environment_classes (
		name, description, allowed_regions, requires_approval, max_ttl_hours, default_namespaces, quota_profile, policy_packs, estimated_monthly_cost_usd
	) values ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	on conflict (name) do update set
		description = excluded.description,
		allowed_regions = excluded.allowed_regions,
		requires_approval = excluded.requires_approval,
		max_ttl_hours = excluded.max_ttl_hours,
		default_namespaces = excluded.default_namespaces,
		quota_profile = excluded.quota_profile,
		policy_packs = excluded.policy_packs,
		estimated_monthly_cost_usd = excluded.estimated_monthly_cost_usd
	`

	for _, class := range classes {
		if _, err := r.pool.Exec(ctx, query,
			class.Name,
			class.Description,
			class.AllowedRegions,
			class.RequiresApproval,
			class.MaxTTLHours,
			class.DefaultNamespaces,
			class.QuotaProfile,
			class.PolicyPacks,
			class.EstimatedMonthlyCostUSD,
		); err != nil {
			return fmt.Errorf("upsert environment class %s: %w", class.Name, err)
		}
	}

	return nil
}

func (r *PostgresRepository) ListClasses(ctx context.Context) ([]domain.EnvironmentClass, error) {
	rows, err := r.pool.Query(ctx, `
		select name, description, allowed_regions, requires_approval, max_ttl_hours, default_namespaces, quota_profile, policy_packs, estimated_monthly_cost_usd
		from environment_classes
		order by name
	`)
	if err != nil {
		return nil, fmt.Errorf("list environment classes: %w", err)
	}
	defer rows.Close()

	items, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (domain.EnvironmentClass, error) {
		var item domain.EnvironmentClass
		err := row.Scan(
			&item.Name,
			&item.Description,
			&item.AllowedRegions,
			&item.RequiresApproval,
			&item.MaxTTLHours,
			&item.DefaultNamespaces,
			&item.QuotaProfile,
			&item.PolicyPacks,
			&item.EstimatedMonthlyCostUSD,
		)
		return item, err
	})
	if err != nil {
		return nil, fmt.Errorf("scan environment classes: %w", err)
	}

	return items, nil
}

func (r *PostgresRepository) GetClass(ctx context.Context, name string) (domain.EnvironmentClass, error) {
	var item domain.EnvironmentClass
	err := r.pool.QueryRow(ctx, `
		select name, description, allowed_regions, requires_approval, max_ttl_hours, default_namespaces, quota_profile, policy_packs, estimated_monthly_cost_usd
		from environment_classes
		where name = $1
	`, name).Scan(
		&item.Name,
		&item.Description,
		&item.AllowedRegions,
		&item.RequiresApproval,
		&item.MaxTTLHours,
		&item.DefaultNamespaces,
		&item.QuotaProfile,
		&item.PolicyPacks,
		&item.EstimatedMonthlyCostUSD,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return domain.EnvironmentClass{}, ErrNotFound
		}
		return domain.EnvironmentClass{}, fmt.Errorf("get environment class %s: %w", name, err)
	}

	return item, nil
}

func (r *PostgresRepository) CreateRequest(ctx context.Context, req domain.EnvironmentRequest) (domain.EnvironmentRequest, error) {
	labels, err := json.Marshal(req.Labels)
	if err != nil {
		return domain.EnvironmentRequest{}, fmt.Errorf("marshal request labels: %w", err)
	}

	const query = `
	insert into environment_requests (
		id, app, team, class, region, ttl_hours, owner, repository, revision, labels,
		namespace, gitops_path, last_error, status, version, created_at, queued_at, approved_at, approved_by, approval_signature, last_reconciled_at, git_commit_sha, git_branch,
		git_promotion_mode, git_promotion_branch, git_promotion_url, cluster_status, drift_status, drift_summary, quota_profile, policy_packs, estimated_monthly_cost_usd
	) values (
		$1, $2, $3, $4, $5, $6, $7, $8, $9, $10::jsonb,
		$11, $12, $13, $14, 1, $15, $16, $17, $18, $19, $20, $21, $22,
		$23, $24, $25, $26, $27, $28, $29, $30, $31
	)
	returning version
	`

	err = r.pool.QueryRow(ctx, query,
		req.ID,
		req.App,
		req.Team,
		req.Class,
		req.Region,
		req.TTLHours,
		req.Owner,
		req.Repository,
		req.Revision,
		labels,
		req.Namespace,
		req.GitOpsPath,
		req.LastError,
		req.Status,
		req.CreatedAt,
		req.QueuedAt,
		req.ApprovedAt,
		req.ApprovedBy,
		req.ApprovalSignature,
		req.LastReconciledAt,
		req.GitCommitSHA,
		req.GitBranch,
		req.GitPromotionMode,
		req.GitPromotionBranch,
		req.GitPromotionURL,
		req.ClusterStatus,
		req.DriftStatus,
		req.DriftSummary,
		req.QuotaProfile,
		req.PolicyPacks,
		req.EstimatedMonthlyCostUSD,
	).Scan(&req.Version)
	if err != nil {
		return domain.EnvironmentRequest{}, fmt.Errorf("create environment request %s: %w", req.ID, err)
	}

	return req, nil
}

func (r *PostgresRepository) UpdateRequest(ctx context.Context, req domain.EnvironmentRequest) (domain.EnvironmentRequest, error) {
	labels, err := json.Marshal(req.Labels)
	if err != nil {
		return domain.EnvironmentRequest{}, fmt.Errorf("marshal request labels: %w", err)
	}

	const query = `
	update environment_requests
	set app = $2,
		team = $3,
		class = $4,
		region = $5,
		ttl_hours = $6,
		owner = $7,
		repository = $8,
		revision = $9,
		labels = $10::jsonb,
		namespace = $11,
		gitops_path = $12,
		last_error = $13,
		status = $14,
		version = version + 1,
		queued_at = $15,
		approved_at = $16,
		approved_by = $17,
		approval_signature = $18,
		last_reconciled_at = $19,
		git_commit_sha = $20,
		git_branch = $21,
		git_promotion_mode = $22,
		git_promotion_branch = $23,
		git_promotion_url = $24,
		cluster_status = $25,
		drift_status = $26,
		drift_summary = $27,
		quota_profile = $28,
		policy_packs = $29,
		estimated_monthly_cost_usd = $30
	where id = $1
	returning version
	`

	err = r.pool.QueryRow(ctx, query,
		req.ID,
		req.App,
		req.Team,
		req.Class,
		req.Region,
		req.TTLHours,
		req.Owner,
		req.Repository,
		req.Revision,
		labels,
		req.Namespace,
		req.GitOpsPath,
		req.LastError,
		req.Status,
		req.QueuedAt,
		req.ApprovedAt,
		req.ApprovedBy,
		req.ApprovalSignature,
		req.LastReconciledAt,
		req.GitCommitSHA,
		req.GitBranch,
		req.GitPromotionMode,
		req.GitPromotionBranch,
		req.GitPromotionURL,
		req.ClusterStatus,
		req.DriftStatus,
		req.DriftSummary,
		req.QuotaProfile,
		req.PolicyPacks,
		req.EstimatedMonthlyCostUSD,
	).Scan(&req.Version)
	if err != nil {
		if err == pgx.ErrNoRows {
			return domain.EnvironmentRequest{}, ErrNotFound
		}
		return domain.EnvironmentRequest{}, fmt.Errorf("update environment request %s: %w", req.ID, err)
	}

	return req, nil
}

func (r *PostgresRepository) GetRequest(ctx context.Context, id string) (domain.EnvironmentRequest, error) {
	row := r.pool.QueryRow(ctx, `
		select id, app, team, class, region, ttl_hours, owner, repository, revision, labels,
		       namespace, gitops_path, last_error, status, version, created_at, queued_at, approved_at, approved_by, approval_signature, last_reconciled_at, git_commit_sha, git_branch,
		       git_promotion_mode, git_promotion_branch, git_promotion_url, cluster_status, drift_status, drift_summary, quota_profile, policy_packs, estimated_monthly_cost_usd
		from environment_requests
		where id = $1
	`, id)

	req, err := scanRequestRow(row)
	if err != nil {
		if err == pgx.ErrNoRows {
			return domain.EnvironmentRequest{}, ErrNotFound
		}
		return domain.EnvironmentRequest{}, fmt.Errorf("get environment request %s: %w", id, err)
	}

	return req, nil
}

func (r *PostgresRepository) ListRequests(ctx context.Context) ([]domain.EnvironmentRequest, error) {
	rows, err := r.pool.Query(ctx, `
		select id, app, team, class, region, ttl_hours, owner, repository, revision, labels,
		       namespace, gitops_path, last_error, status, version, created_at, queued_at, approved_at, approved_by, approval_signature, last_reconciled_at, git_commit_sha, git_branch,
		       git_promotion_mode, git_promotion_branch, git_promotion_url, cluster_status, drift_status, drift_summary, quota_profile, policy_packs, estimated_monthly_cost_usd
		from environment_requests
		order by created_at
	`)
	if err != nil {
		return nil, fmt.Errorf("list environment requests: %w", err)
	}
	defer rows.Close()

	items, err := pgx.CollectRows(rows, scanRequest)
	if err != nil {
		return nil, fmt.Errorf("scan environment requests: %w", err)
	}

	return items, nil
}

func (r *PostgresRepository) ensureSchema(ctx context.Context) error {
	if err := migrate.Run(ctx, r.pool); err != nil {
		return fmt.Errorf("ensure postgres schema: %w", err)
	}

	return nil
}

func scanRequest(row pgx.CollectableRow) (domain.EnvironmentRequest, error) {
	return scanRequestValues(row)
}

func scanRequestRow(row pgx.Row) (domain.EnvironmentRequest, error) {
	return scanRequestValues(row)
}

func scanRequestValues(row interface {
	Scan(dest ...any) error
}) (domain.EnvironmentRequest, error) {
	var (
		req        domain.EnvironmentRequest
		labelData  []byte
		policyPack []string
	)

	err := row.Scan(
		&req.ID,
		&req.App,
		&req.Team,
		&req.Class,
		&req.Region,
		&req.TTLHours,
		&req.Owner,
		&req.Repository,
		&req.Revision,
		&labelData,
		&req.Namespace,
		&req.GitOpsPath,
		&req.LastError,
		&req.Status,
		&req.Version,
		&req.CreatedAt,
		&req.QueuedAt,
		&req.ApprovedAt,
		&req.ApprovedBy,
		&req.ApprovalSignature,
		&req.LastReconciledAt,
		&req.GitCommitSHA,
		&req.GitBranch,
		&req.GitPromotionMode,
		&req.GitPromotionBranch,
		&req.GitPromotionURL,
		&req.ClusterStatus,
		&req.DriftStatus,
		&req.DriftSummary,
		&req.QuotaProfile,
		&policyPack,
		&req.EstimatedMonthlyCostUSD,
	)
	if err != nil {
		return domain.EnvironmentRequest{}, err
	}

	if len(labelData) > 0 {
		if err := json.Unmarshal(labelData, &req.Labels); err != nil {
			return domain.EnvironmentRequest{}, fmt.Errorf("unmarshal request labels: %w", err)
		}
	}
	req.PolicyPacks = policyPack

	return req, nil
}

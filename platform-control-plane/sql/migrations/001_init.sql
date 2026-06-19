create table if not exists environment_classes (
    name text primary key,
    description text not null,
    allowed_regions text[] not null,
    requires_approval boolean not null,
    max_ttl_hours integer not null,
    default_namespaces integer not null,
    quota_profile text not null default 'small',
    policy_packs text[] not null default '{}'::text[],
    estimated_monthly_cost_usd integer not null default 0
);

create table if not exists environment_requests (
    id text primary key,
    app text not null,
    team text not null,
    class text not null references environment_classes(name),
    region text not null,
    ttl_hours integer not null,
    owner text not null,
    repository text not null,
    revision text not null,
    labels jsonb not null default '{}'::jsonb,
    namespace text not null default '',
    gitops_path text not null default '',
    last_error text not null default '',
    status text not null,
    version integer not null default 1,
    created_at timestamptz not null,
    queued_at timestamptz null,
    approved_at timestamptz null,
    approved_by text not null default '',
    approval_signature text not null default '',
    last_reconciled_at timestamptz null
    ,
    git_commit_sha text not null default '',
    git_branch text not null default '',
    git_promotion_mode text not null default '',
    git_promotion_branch text not null default '',
    git_promotion_url text not null default '',
    cluster_status text not null default '',
    drift_status text not null default '',
    drift_summary text not null default '',
    quota_profile text not null default '',
    policy_packs text[] not null default '{}'::text[],
    estimated_monthly_cost_usd integer not null default 0
);

create index if not exists environment_requests_team_idx on environment_requests(team);
create index if not exists environment_requests_status_idx on environment_requests(status);

create table if not exists reconcile_jobs (
    id bigserial primary key,
    request_id text not null unique references environment_requests(id) on delete cascade,
    status text not null,
    attempts integer not null default 0,
    max_attempts integer not null default 5,
    next_run_at timestamptz not null default now(),
    leased_until timestamptz null,
    worker_id text not null default '',
    last_error text not null default '',
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now()
);

create index if not exists reconcile_jobs_status_next_run_idx on reconcile_jobs(status, next_run_at);

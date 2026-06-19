alter table environment_classes
    add column if not exists quota_profile text not null default 'small',
    add column if not exists policy_packs text[] not null default '{}'::text[],
    add column if not exists estimated_monthly_cost_usd integer not null default 0;

alter table environment_requests
    add column if not exists git_promotion_mode text not null default '',
    add column if not exists git_promotion_branch text not null default '',
    add column if not exists git_promotion_url text not null default '',
    add column if not exists cluster_status text not null default '',
    add column if not exists drift_status text not null default '',
    add column if not exists drift_summary text not null default '',
    add column if not exists quota_profile text not null default '',
    add column if not exists policy_packs text[] not null default '{}'::text[],
    add column if not exists estimated_monthly_cost_usd integer not null default 0;

BEGIN;

CREATE TABLE managed_resources (
    id UUID PRIMARY KEY,
    kind TEXT NOT NULL CHECK (kind IN ('cloud_run_service')),
    migration_id UUID NOT NULL REFERENCES migrations(id) ON DELETE CASCADE,
    source_plan_id UUID NOT NULL REFERENCES terraform_plans(id) ON DELETE RESTRICT,
    project_id TEXT NOT NULL,
    region TEXT NOT NULL,
    name TEXT NOT NULL,
    desired_state JSONB NOT NULL,
    observed_state JSONB,
    state TEXT NOT NULL CHECK (state IN ('unknown', 'in_sync', 'drifted', 'missing', 'error')),
    remediation_policy TEXT NOT NULL CHECK (remediation_policy IN ('report', 'automatic')),
    generation BIGINT NOT NULL CHECK (generation > 0),
    observed_generation BIGINT NOT NULL DEFAULT 0 CHECK (observed_generation >= 0),
    conditions JSONB NOT NULL DEFAULT '[]'::jsonb,
    retry_count INTEGER NOT NULL DEFAULT 0 CHECK (retry_count >= 0),
    next_reconcile_at TIMESTAMPTZ NOT NULL,
    last_reconciled_at TIMESTAMPTZ,
    claimed_by TEXT,
    lease_expires_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    UNIQUE (kind, project_id, region, name)
);

CREATE INDEX managed_resources_reconcile_idx
    ON managed_resources (next_reconcile_at, lease_expires_at, updated_at);

COMMIT;

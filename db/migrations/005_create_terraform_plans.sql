BEGIN;

CREATE TABLE terraform_plans (
    id UUID PRIMARY KEY,
    migration_id UUID NOT NULL REFERENCES migrations(id) ON DELETE CASCADE,
    idempotency_key TEXT NOT NULL,
    request_hash CHAR(64) NOT NULL,
    status TEXT NOT NULL CHECK (
        status IN ('queued', 'planning', 'ready', 'rejected', 'approved', 'failed')
    ),
    specification JSONB NOT NULL,
    policy JSONB NOT NULL,
    has_changes BOOLEAN,
    artifact_json_path TEXT,
    artifact_text_path TEXT,
    artifact_json_sha256 CHAR(64),
    artifact_text_sha256 CHAR(64),
    artifact_created_at TIMESTAMPTZ,
    failure_message TEXT NOT NULL DEFAULT '',
    attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    approved_by TEXT,
    approved_at TIMESTAMPTZ,
    claimed_by TEXT,
    lease_expires_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    UNIQUE (migration_id, idempotency_key)
);

CREATE INDEX terraform_plans_claimable_idx
    ON terraform_plans (status, lease_expires_at, created_at);

CREATE TABLE terraform_plan_approvals (
    id BIGSERIAL PRIMARY KEY,
    plan_id UUID NOT NULL REFERENCES terraform_plans(id) ON DELETE CASCADE,
    actor TEXT NOT NULL,
    artifact_json_sha256 CHAR(64) NOT NULL,
    artifact_text_sha256 CHAR(64) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX terraform_plan_approvals_plan_created_idx
    ON terraform_plan_approvals (plan_id, created_at);

COMMIT;

BEGIN;

ALTER TABLE terraform_plans
    DROP CONSTRAINT terraform_plans_status_check;

ALTER TABLE terraform_plans
    ADD CONSTRAINT terraform_plans_status_check CHECK (
        status IN (
            'queued', 'planning', 'ready', 'rejected', 'approved', 'failed',
            'apply_queued', 'applying', 'applied', 'apply_failed'
        )
    ),
    ADD COLUMN artifact_binary_object_key TEXT,
    ADD COLUMN artifact_binary_sha256 CHAR(64),
    ADD COLUMN apply_attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (apply_attempt_count >= 0),
    ADD COLUMN apply_requested_by TEXT,
    ADD COLUMN apply_requested_at TIMESTAMPTZ;

ALTER TABLE terraform_plan_approvals
    ADD COLUMN artifact_binary_sha256 CHAR(64);

CREATE TABLE terraform_apply_requests (
    id BIGSERIAL PRIMARY KEY,
    plan_id UUID NOT NULL REFERENCES terraform_plans(id) ON DELETE CASCADE,
    actor TEXT NOT NULL,
    artifact_binary_sha256 CHAR(64) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX terraform_apply_requests_plan_created_idx
    ON terraform_apply_requests (plan_id, created_at);

COMMIT;

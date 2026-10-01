BEGIN;

ALTER TABLE managed_resources
    ADD COLUMN lifecycle TEXT NOT NULL DEFAULT 'active'
        CHECK (lifecycle IN ('active', 'deletion_requested', 'delete_failed', 'deleted')),
    ADD COLUMN deletion_requested_by TEXT,
    ADD COLUMN deletion_requested_at TIMESTAMPTZ,
    ADD COLUMN deleted_at TIMESTAMPTZ,
    ADD COLUMN delete_failure TEXT;

ALTER TABLE managed_resources
    ADD CONSTRAINT managed_resources_deletion_request_complete CHECK (
        (deletion_requested_at IS NULL AND deletion_requested_by IS NULL)
        OR (deletion_requested_at IS NOT NULL AND deletion_requested_by IS NOT NULL)
    );

CREATE INDEX managed_resources_lifecycle_idx
    ON managed_resources (lifecycle, next_reconcile_at, lease_expires_at);

COMMIT;

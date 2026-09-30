BEGIN;

ALTER TABLE migrations
    ADD COLUMN claimed_by TEXT,
    ADD COLUMN lease_expires_at TIMESTAMPTZ;

CREATE INDEX migrations_claimable_idx
    ON migrations (status, lease_expires_at, created_at);

COMMIT;

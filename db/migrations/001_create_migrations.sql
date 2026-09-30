BEGIN;

CREATE TABLE migrations (
    id UUID PRIMARY KEY,
    idempotency_key TEXT NOT NULL UNIQUE,
    request_hash CHAR(64) NOT NULL,
    status TEXT NOT NULL CHECK (
        status IN ('queued', 'running', 'cancelling', 'succeeded', 'failed', 'cancelled')
    ),
    source_repository_url TEXT NOT NULL,
    source_revision TEXT NOT NULL,
    destination_provider TEXT NOT NULL,
    destination_project_id TEXT NOT NULL,
    destination_region TEXT NOT NULL,
    destination_runtime TEXT NOT NULL,
    destination_database TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX migrations_status_created_at_idx
    ON migrations (status, created_at);

COMMIT;

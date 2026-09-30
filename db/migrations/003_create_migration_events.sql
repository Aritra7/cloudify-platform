BEGIN;

CREATE TABLE migration_events (
    sequence BIGSERIAL PRIMARY KEY,
    migration_id UUID NOT NULL REFERENCES migrations(id) ON DELETE CASCADE,
    kind TEXT NOT NULL,
    phase TEXT NOT NULL DEFAULT '',
    stream TEXT NOT NULL DEFAULT '',
    message TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX migration_events_migration_sequence_idx
    ON migration_events (migration_id, sequence);

COMMIT;

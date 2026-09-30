BEGIN;

CREATE TABLE resource_reconciliation_events (
    sequence BIGSERIAL PRIMARY KEY,
    resource_id UUID NOT NULL REFERENCES managed_resources(id),
    generation BIGINT NOT NULL,
    observed_generation BIGINT NOT NULL,
    observed_state JSONB,
    state TEXT NOT NULL CHECK (state IN ('unknown', 'in_sync', 'drifted', 'missing', 'error')),
    conditions JSONB NOT NULL,
    retry_count INTEGER NOT NULL CHECK (retry_count >= 0),
    created_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX resource_reconciliation_events_resource_sequence_idx
    ON resource_reconciliation_events (resource_id, sequence);

COMMIT;

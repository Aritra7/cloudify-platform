package resources

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

type PostgresStore struct{ db *sql.DB }

func NewPostgresStore(db *sql.DB) *PostgresStore { return &PostgresStore{db: db} }

func (store *PostgresStore) UpsertApplied(ctx context.Context, candidate Resource) (Resource, bool, error) {
	desired, err := json.Marshal(candidate.Desired)
	if err != nil {
		return Resource{}, false, fmt.Errorf("encode managed resource desired state: %w", err)
	}
	conditions, err := json.Marshal(candidate.Conditions)
	if err != nil {
		return Resource{}, false, fmt.Errorf("encode managed resource conditions: %w", err)
	}
	resource, err := scanResource(store.db.QueryRowContext(ctx, `
		INSERT INTO managed_resources (
			id, kind, migration_id, source_plan_id, project_id, region, name,
			desired_state, state, remediation_policy, generation, observed_generation,
			conditions, retry_count, next_reconcile_at, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, 0, $12, 0, $13, $14, $14)
		ON CONFLICT (id) DO UPDATE SET
			migration_id = EXCLUDED.migration_id,
			source_plan_id = EXCLUDED.source_plan_id,
			desired_state = EXCLUDED.desired_state,
			generation = managed_resources.generation + 1,
			observed_generation = 0,
			state = 'unknown', conditions = '[]'::jsonb, retry_count = 0,
			next_reconcile_at = EXCLUDED.next_reconcile_at, updated_at = EXCLUDED.updated_at
		WHERE managed_resources.source_plan_id <> EXCLUDED.source_plan_id
		RETURNING `+resourceColumns,
		candidate.ID, candidate.Kind, candidate.MigrationID, candidate.SourcePlanID,
		candidate.ProjectID, candidate.Region, candidate.Name, desired, candidate.State,
		candidate.RemediationPolicy, candidate.Generation, conditions, candidate.NextReconcileAt, candidate.CreatedAt,
	))
	if err == nil {
		return resource, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Resource{}, false, fmt.Errorf("upsert managed resource: %w", err)
	}
	existing, err := store.Get(ctx, candidate.ID)
	return existing, false, err
}

func (store *PostgresStore) Get(ctx context.Context, id string) (Resource, error) {
	resource, err := scanResource(store.db.QueryRowContext(ctx, `SELECT `+resourceColumns+` FROM managed_resources WHERE id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Resource{}, ErrNotFound
	}
	if err != nil {
		return Resource{}, fmt.Errorf("read managed resource: %w", err)
	}
	return resource, nil
}

func (store *PostgresStore) List(ctx context.Context, limit int) ([]Resource, error) {
	rows, err := store.db.QueryContext(ctx, `SELECT `+resourceColumns+` FROM managed_resources ORDER BY updated_at DESC, id LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list managed resources: %w", err)
	}
	defer rows.Close()
	result := make([]Resource, 0)
	for rows.Next() {
		resource, err := scanResource(rows)
		if err != nil {
			return nil, fmt.Errorf("scan managed resource: %w", err)
		}
		result = append(result, resource)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list managed resources: %w", err)
	}
	return result, nil
}

const resourceColumns = `
	id, kind, migration_id::text, source_plan_id::text, project_id, region, name,
	desired_state, observed_state, state, remediation_policy, generation, observed_generation,
	conditions, retry_count, next_reconcile_at, last_reconciled_at,
	created_at, updated_at, claimed_by, lease_expires_at`

func scanResource(row interface{ Scan(...any) error }) (Resource, error) {
	var resource Resource
	var desired []byte
	var observed, conditions []byte
	var lastReconciledAt, leaseExpiresAt sql.NullTime
	var claimedBy sql.NullString
	if err := row.Scan(
		&resource.ID, &resource.Kind, &resource.MigrationID, &resource.SourcePlanID,
		&resource.ProjectID, &resource.Region, &resource.Name, &desired, &observed,
		&resource.State, &resource.RemediationPolicy, &resource.Generation, &resource.ObservedGeneration,
		&conditions, &resource.RetryCount, &resource.NextReconcileAt, &lastReconciledAt,
		&resource.CreatedAt, &resource.UpdatedAt, &claimedBy, &leaseExpiresAt,
	); err != nil {
		return Resource{}, err
	}
	if err := json.Unmarshal(desired, &resource.Desired); err != nil {
		return Resource{}, fmt.Errorf("decode desired state: %w", err)
	}
	if len(observed) > 0 {
		resource.Observed = append(json.RawMessage(nil), observed...)
	}
	if err := json.Unmarshal(conditions, &resource.Conditions); err != nil {
		return Resource{}, fmt.Errorf("decode resource conditions: %w", err)
	}
	if resource.Conditions == nil {
		resource.Conditions = []Condition{}
	}
	if lastReconciledAt.Valid {
		resource.LastReconciledAt = &lastReconciledAt.Time
	}
	if claimedBy.Valid {
		resource.ClaimedBy = claimedBy.String
	}
	if leaseExpiresAt.Valid {
		resource.LeaseExpiresAt = &leaseExpiresAt.Time
	}
	return resource, nil
}

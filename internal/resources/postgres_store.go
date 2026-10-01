package resources

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
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
			generation = managed_resources.generation + CASE
				WHEN managed_resources.desired_state IS DISTINCT FROM EXCLUDED.desired_state THEN 1 ELSE 0 END,
			observed_generation = 0,
			state = 'unknown', conditions = '[]'::jsonb, retry_count = 0,
			lifecycle = 'active', deletion_requested_by = NULL,
			deletion_requested_at = NULL, deleted_at = NULL, delete_failure = NULL,
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

func (store *PostgresStore) RequestDeletion(ctx context.Context, id, actor string, now time.Time) (Resource, error) {
	resource, err := scanResource(store.db.QueryRowContext(ctx, `
		UPDATE managed_resources SET
			lifecycle = CASE WHEN lifecycle = 'deleted' THEN lifecycle ELSE 'deletion_requested' END,
			deletion_requested_by = CASE WHEN lifecycle = 'deleted' THEN deletion_requested_by ELSE $2 END,
			deletion_requested_at = CASE WHEN lifecycle = 'deleted' THEN deletion_requested_at ELSE $3 END,
			deleted_at = CASE WHEN lifecycle = 'deleted' THEN deleted_at ELSE NULL END,
			delete_failure = CASE WHEN lifecycle = 'deleted' THEN delete_failure ELSE NULL END,
			next_reconcile_at = CASE WHEN lifecycle = 'deleted' THEN next_reconcile_at ELSE $3 END,
			updated_at = $3
		WHERE id = $1 RETURNING `+resourceColumns, id, actor, now))
	if errors.Is(err, sql.ErrNoRows) {
		return Resource{}, ErrNotFound
	}
	if err != nil {
		return Resource{}, fmt.Errorf("request managed resource deletion: %w", err)
	}
	return resource, nil
}

func (store *PostgresStore) CompleteDeletion(
	ctx context.Context, id, workerID, failure string, next, now time.Time,
) (Resource, error) {
	lifecycle := LifecycleDeleted
	state := StateMissing
	retryIncrement := 0
	conditionValue := "True"
	reason := "TerraformDestroyApplied"
	message := "managed infrastructure was destroyed"
	var deletedAt any = now
	if failure != "" {
		lifecycle = LifecycleDeleteFailed
		state = StateError
		retryIncrement = 1
		conditionValue = "False"
		reason = "TerraformDestroyFailed"
		message = "Terraform destroy failed; retry is scheduled"
		deletedAt = nil
	}
	conditions, err := json.Marshal([]Condition{condition("Deleted", conditionValue, reason, message, now)})
	if err != nil {
		return Resource{}, fmt.Errorf("encode deletion condition: %w", err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return Resource{}, fmt.Errorf("begin managed resource deletion completion: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	resource, err := scanResource(tx.QueryRowContext(ctx, `
		UPDATE managed_resources SET lifecycle = $3, state = $4, conditions = $5,
			retry_count = retry_count + $6, next_reconcile_at = $7,
			last_reconciled_at = $8, deleted_at = $9, delete_failure = NULLIF($10, ''),
			claimed_by = NULL, lease_expires_at = NULL, updated_at = $8
		WHERE id = $1 AND claimed_by = $2
		RETURNING `+resourceColumns,
		id, workerID, lifecycle, state, conditions, retryIncrement, next, now, deletedAt, failure,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return Resource{}, ErrLeaseLost
	}
	if err != nil {
		return Resource{}, fmt.Errorf("complete managed resource deletion: %w", err)
	}
	var observed any
	if len(resource.Observed) > 0 {
		observed = []byte(resource.Observed)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO resource_reconciliation_events (
			resource_id, generation, observed_generation, observed_state,
			state, conditions, retry_count, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		id, resource.Generation, resource.ObservedGeneration, observed,
		resource.State, conditions, resource.RetryCount, now,
	); err != nil {
		return Resource{}, fmt.Errorf("append deletion event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Resource{}, fmt.Errorf("commit managed resource deletion: %w", err)
	}
	return resource, nil
}

func (store *PostgresStore) ClaimNext(ctx context.Context, workerID string, now, leaseUntil time.Time) (Resource, bool, error) {
	resource, err := scanResource(store.db.QueryRowContext(ctx, `
		WITH candidate AS (
			SELECT id FROM managed_resources
			WHERE next_reconcile_at <= $2
			  AND lifecycle <> 'deleted'
			  AND (claimed_by IS NULL OR lease_expires_at <= $2)
			ORDER BY next_reconcile_at, updated_at
			FOR UPDATE SKIP LOCKED LIMIT 1
		)
		UPDATE managed_resources AS resource
		SET claimed_by = $1, lease_expires_at = $3, updated_at = $2
		FROM candidate WHERE resource.id = candidate.id
		RETURNING `+qualifiedResourceColumns("resource"), workerID, now, leaseUntil))
	if errors.Is(err, sql.ErrNoRows) {
		return Resource{}, false, nil
	}
	if err != nil {
		return Resource{}, false, fmt.Errorf("claim managed resource: %w", err)
	}
	return resource, true, nil
}

func (store *PostgresStore) RenewLease(ctx context.Context, id, workerID string, now, leaseUntil time.Time) error {
	result, err := store.db.ExecContext(ctx, `
		UPDATE managed_resources SET lease_expires_at = $4, updated_at = $3
		WHERE id = $1 AND claimed_by = $2 AND lease_expires_at > $3`, id, workerID, now, leaseUntil)
	if err != nil {
		return fmt.Errorf("renew managed resource lease: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read managed resource lease count: %w", err)
	}
	if rows != 1 {
		return ErrLeaseLost
	}
	return nil
}

func (store *PostgresStore) Complete(
	ctx context.Context, id, workerID string, result ReconcileResult, now time.Time,
) (Resource, error) {
	conditions, err := json.Marshal(result.Conditions)
	if err != nil {
		return Resource{}, fmt.Errorf("encode reconciliation conditions: %w", err)
	}
	var observed any
	if len(result.Observed) > 0 {
		observed = []byte(result.Observed)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return Resource{}, fmt.Errorf("begin managed resource completion: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	resource, err := scanResource(tx.QueryRowContext(ctx, `
		UPDATE managed_resources SET observed_state = $3, state = $4, observed_generation = $5,
		conditions = $6, retry_count = $7, next_reconcile_at = $8, last_reconciled_at = $9,
		claimed_by = NULL, lease_expires_at = NULL, updated_at = $10
		WHERE id = $1 AND claimed_by = $2
		RETURNING `+resourceColumns,
		id, workerID, observed, result.State, result.ObservedGeneration, conditions,
		result.RetryCount, result.NextReconcileAt, result.LastReconciledAt, now,
	))
	if errors.Is(err, sql.ErrNoRows) {
		var exists bool
		if getErr := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM managed_resources WHERE id = $1)`, id).Scan(&exists); getErr != nil {
			return Resource{}, fmt.Errorf("check managed resource after stale completion: %w", getErr)
		}
		if !exists {
			return Resource{}, ErrNotFound
		}
		return Resource{}, ErrLeaseLost
	}
	if err != nil {
		return Resource{}, fmt.Errorf("complete managed resource reconciliation: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO resource_reconciliation_events (
			resource_id, generation, observed_generation, observed_state,
			state, conditions, retry_count, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		id, resource.Generation, result.ObservedGeneration, observed,
		result.State, conditions, result.RetryCount, result.LastReconciledAt,
	); err != nil {
		return Resource{}, fmt.Errorf("append reconciliation event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Resource{}, fmt.Errorf("commit managed resource reconciliation: %w", err)
	}
	return resource, nil
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

func (store *PostgresStore) ListEvents(ctx context.Context, id string, after int64, limit int) ([]Event, error) {
	rows, err := store.db.QueryContext(ctx, `
		SELECT sequence, resource_id::text, generation, observed_generation, observed_state,
		state, conditions, retry_count, created_at
		FROM resource_reconciliation_events
		WHERE resource_id = $1 AND sequence > $2 ORDER BY sequence LIMIT $3`, id, after, limit)
	if err != nil {
		return nil, fmt.Errorf("list reconciliation events: %w", err)
	}
	result := make([]Event, 0)
	for rows.Next() {
		event, err := scanEvent(rows)
		if err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan reconciliation event: %w", err)
		}
		result = append(result, event)
	}
	rowsErr := rows.Err()
	_ = rows.Close()
	if rowsErr != nil {
		return nil, fmt.Errorf("list reconciliation events: %w", rowsErr)
	}
	if len(result) == 0 {
		if _, err := store.Get(ctx, id); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func scanEvent(row interface{ Scan(...any) error }) (Event, error) {
	var event Event
	var observed, conditions []byte
	if err := row.Scan(
		&event.Sequence, &event.ResourceID, &event.Generation, &event.ObservedGeneration,
		&observed, &event.State, &conditions, &event.RetryCount, &event.CreatedAt,
	); err != nil {
		return Event{}, err
	}
	if len(observed) > 0 {
		event.Observed = append(json.RawMessage(nil), observed...)
	}
	if err := json.Unmarshal(conditions, &event.Conditions); err != nil {
		return Event{}, fmt.Errorf("decode reconciliation event conditions: %w", err)
	}
	if event.Conditions == nil {
		event.Conditions = []Condition{}
	}
	return event, nil
}

const resourceColumns = `
	id, kind, migration_id::text, source_plan_id::text, project_id, region, name,
	desired_state, observed_state, state, remediation_policy, generation, observed_generation,
	conditions, retry_count, next_reconcile_at, last_reconciled_at,
	created_at, updated_at, lifecycle, deletion_requested_by, deletion_requested_at,
	deleted_at, delete_failure, claimed_by, lease_expires_at`

func qualifiedResourceColumns(alias string) string {
	format := `
	%s.id, %s.kind, %s.migration_id::text, %s.source_plan_id::text, %s.project_id, %s.region, %s.name,
	%s.desired_state, %s.observed_state, %s.state, %s.remediation_policy, %s.generation, %s.observed_generation,
	%s.conditions, %s.retry_count, %s.next_reconcile_at, %s.last_reconciled_at,
	%s.created_at, %s.updated_at, %s.lifecycle, %s.deletion_requested_by, %s.deletion_requested_at,
	%s.deleted_at, %s.delete_failure, %s.claimed_by, %s.lease_expires_at`
	arguments := make([]any, 26)
	for index := range arguments {
		arguments[index] = alias
	}
	return fmt.Sprintf(format, arguments...)
}

func scanResource(row interface{ Scan(...any) error }) (Resource, error) {
	var resource Resource
	var desired []byte
	var observed, conditions []byte
	var lastReconciledAt, deletionRequestedAt, deletedAt, leaseExpiresAt sql.NullTime
	var deletionRequestedBy, deleteFailure, claimedBy sql.NullString
	if err := row.Scan(
		&resource.ID, &resource.Kind, &resource.MigrationID, &resource.SourcePlanID,
		&resource.ProjectID, &resource.Region, &resource.Name, &desired, &observed,
		&resource.State, &resource.RemediationPolicy, &resource.Generation, &resource.ObservedGeneration,
		&conditions, &resource.RetryCount, &resource.NextReconcileAt, &lastReconciledAt,
		&resource.CreatedAt, &resource.UpdatedAt, &resource.Lifecycle,
		&deletionRequestedBy, &deletionRequestedAt, &deletedAt, &deleteFailure,
		&claimedBy, &leaseExpiresAt,
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
	if deletionRequestedBy.Valid {
		resource.DeletionRequestedBy = deletionRequestedBy.String
	}
	if deletionRequestedAt.Valid {
		resource.DeletionRequestedAt = &deletionRequestedAt.Time
	}
	if deletedAt.Valid {
		resource.DeletedAt = &deletedAt.Time
	}
	if deleteFailure.Valid {
		resource.DeleteFailure = deleteFailure.String
	}
	if claimedBy.Valid {
		resource.ClaimedBy = claimedBy.String
	}
	if leaseExpiresAt.Valid {
		resource.LeaseExpiresAt = &leaseExpiresAt.Time
	}
	return resource, nil
}

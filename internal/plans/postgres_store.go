package plans

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Aritra7/cloudify-platform/internal/iac"
)

type PostgresStore struct{ db *sql.DB }

func NewPostgresStore(db *sql.DB) *PostgresStore { return &PostgresStore{db: db} }

func (store *PostgresStore) CreateOrGet(ctx context.Context, candidate Plan) (Plan, bool, error) {
	specification, err := json.Marshal(candidate.Specification)
	if err != nil {
		return Plan{}, false, fmt.Errorf("encode plan specification: %w", err)
	}
	policy, err := json.Marshal(candidate.Policy)
	if err != nil {
		return Plan{}, false, fmt.Errorf("encode plan policy: %w", err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return Plan{}, false, fmt.Errorf("begin plan creation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	created, err := scanPlan(tx.QueryRowContext(ctx, `
		INSERT INTO terraform_plans (
			id, migration_id, idempotency_key, request_hash, status,
			specification, policy, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8)
		ON CONFLICT (migration_id, idempotency_key) DO NOTHING
		RETURNING `+planColumns,
		candidate.ID, candidate.MigrationID, candidate.IdempotencyKey, candidate.RequestHash,
		candidate.Status, string(specification), string(policy), candidate.CreatedAt,
	))
	if err == nil {
		if err := tx.Commit(); err != nil {
			return Plan{}, false, fmt.Errorf("commit plan creation: %w", err)
		}
		return created, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Plan{}, false, fmt.Errorf("create Terraform plan: %w", err)
	}
	existing, err := scanPlan(tx.QueryRowContext(ctx, `
		SELECT `+planColumns+` FROM terraform_plans
		WHERE migration_id = $1 AND idempotency_key = $2`, candidate.MigrationID, candidate.IdempotencyKey))
	if err != nil {
		return Plan{}, false, fmt.Errorf("read idempotent Terraform plan: %w", err)
	}
	if existing.RequestHash != candidate.RequestHash {
		return Plan{}, false, ErrIdempotency
	}
	if err := tx.Commit(); err != nil {
		return Plan{}, false, fmt.Errorf("commit plan replay: %w", err)
	}
	return existing, false, nil
}

func (store *PostgresStore) Get(ctx context.Context, id string) (Plan, error) {
	plan, err := scanPlan(store.db.QueryRowContext(ctx, `SELECT `+planColumns+` FROM terraform_plans WHERE id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Plan{}, ErrNotFound
	}
	if err != nil {
		return Plan{}, fmt.Errorf("read Terraform plan: %w", err)
	}
	return plan, nil
}

func (store *PostgresStore) ClaimNext(ctx context.Context, workerID string, now, leaseUntil time.Time) (Plan, bool, error) {
	plan, err := scanPlan(store.db.QueryRowContext(ctx, `
		WITH candidate AS (
			SELECT id FROM terraform_plans
			WHERE status = 'queued' OR (status = 'planning' AND lease_expires_at <= $2)
			ORDER BY created_at
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		UPDATE terraform_plans AS plan
		SET status = 'planning', claimed_by = $1, lease_expires_at = $3,
		    attempt_count = plan.attempt_count + 1, updated_at = $2,
		    failure_message = ''
		FROM candidate WHERE plan.id = candidate.id
		RETURNING `+qualifiedPlanColumns("plan"), workerID, now, leaseUntil))
	if errors.Is(err, sql.ErrNoRows) {
		return Plan{}, false, nil
	}
	if err != nil {
		return Plan{}, false, fmt.Errorf("claim Terraform plan: %w", err)
	}
	return plan, true, nil
}

func (store *PostgresStore) RenewLease(ctx context.Context, id, workerID string, now, leaseUntil time.Time) error {
	result, err := store.db.ExecContext(ctx, `
		UPDATE terraform_plans SET lease_expires_at = $4, updated_at = $3
		WHERE id = $1 AND claimed_by = $2 AND status = 'planning' AND lease_expires_at > $3`,
		id, workerID, now, leaseUntil)
	if err != nil {
		return fmt.Errorf("renew Terraform plan lease: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read Terraform plan lease count: %w", err)
	}
	if rows != 1 {
		return ErrLeaseLost
	}
	return nil
}

func (store *PostgresStore) Complete(
	ctx context.Context,
	id, workerID string,
	status Status,
	hasChanges bool,
	artifact *iac.ArtifactMetadata,
	failureMessage string,
	now time.Time,
) (Plan, error) {
	if status != StatusReady && status != StatusFailed {
		return Plan{}, ErrInvalidTransition
	}
	if status == StatusReady && !validArtifact(artifact) {
		return Plan{}, ErrInvalidTransition
	}
	var jsonPath, textPath, jsonChecksum, textChecksum any
	var artifactCreatedAt any
	if artifact != nil {
		jsonPath, textPath = artifact.JSONPath, artifact.TextPath
		jsonChecksum, textChecksum = artifact.JSONSHA256, artifact.TextSHA256
		artifactCreatedAt = artifact.CreatedAt
	}
	plan, err := scanPlan(store.db.QueryRowContext(ctx, `
		UPDATE terraform_plans SET
			status = $3, has_changes = CASE WHEN $3::text = 'ready' THEN $4::boolean ELSE NULL::boolean END,
			artifact_json_path = $5, artifact_text_path = $6,
			artifact_json_sha256 = $7, artifact_text_sha256 = $8,
			artifact_created_at = $9, failure_message = $10,
			claimed_by = NULL, lease_expires_at = NULL, updated_at = $11
		WHERE id = $1 AND claimed_by = $2 AND status = 'planning'
		RETURNING `+planColumns,
		id, workerID, status, hasChanges, jsonPath, textPath, jsonChecksum, textChecksum,
		artifactCreatedAt, failureMessage, now,
	))
	if errors.Is(err, sql.ErrNoRows) {
		if _, getErr := store.Get(ctx, id); errors.Is(getErr, ErrNotFound) {
			return Plan{}, ErrNotFound
		}
		return Plan{}, ErrLeaseLost
	}
	if err != nil {
		return Plan{}, fmt.Errorf("complete Terraform plan: %w", err)
	}
	return plan, nil
}

func (store *PostgresStore) Approve(ctx context.Context, id, actor string, now time.Time) (Plan, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return Plan{}, fmt.Errorf("begin Terraform plan approval: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	current, err := scanPlan(tx.QueryRowContext(ctx, `SELECT `+planColumns+` FROM terraform_plans WHERE id = $1 FOR UPDATE`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Plan{}, ErrNotFound
	}
	if err != nil {
		return Plan{}, fmt.Errorf("lock Terraform plan for approval: %w", err)
	}
	if current.Status != StatusReady || !current.Policy.Allowed || !validArtifact(current.Artifact) {
		return Plan{}, ErrInvalidTransition
	}
	plan, err := scanPlan(tx.QueryRowContext(ctx, `
		UPDATE terraform_plans
		SET status = 'approved', approved_by = $2, approved_at = $3, updated_at = $3
		WHERE id = $1
		RETURNING `+planColumns, id, actor, now))
	if err != nil {
		return Plan{}, fmt.Errorf("approve Terraform plan: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO terraform_plan_approvals (
			plan_id, actor, artifact_json_sha256, artifact_text_sha256, created_at
		) VALUES ($1, $2, $3, $4, $5)`,
		id, actor, current.Artifact.JSONSHA256, current.Artifact.TextSHA256, now,
	); err != nil {
		return Plan{}, fmt.Errorf("record Terraform plan approval audit: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Plan{}, fmt.Errorf("commit Terraform plan approval: %w", err)
	}
	return plan, nil
}

const planColumns = `
	id::text, migration_id::text, status, specification, policy, has_changes,
	artifact_json_path, artifact_text_path, artifact_json_sha256, artifact_text_sha256,
	artifact_created_at, failure_message, attempt_count, approved_by, approved_at,
	created_at, updated_at, idempotency_key, request_hash, claimed_by, lease_expires_at`

func qualifiedPlanColumns(alias string) string {
	return fmt.Sprintf(`
	%s.id::text, %s.migration_id::text, %s.status, %s.specification, %s.policy, %s.has_changes,
	%s.artifact_json_path, %s.artifact_text_path, %s.artifact_json_sha256, %s.artifact_text_sha256,
	%s.artifact_created_at, %s.failure_message, %s.attempt_count, %s.approved_by, %s.approved_at,
	%s.created_at, %s.updated_at, %s.idempotency_key, %s.request_hash, %s.claimed_by, %s.lease_expires_at`,
		alias, alias, alias, alias, alias, alias, alias, alias, alias, alias, alias,
		alias, alias, alias, alias, alias, alias, alias, alias, alias, alias)
}

type rowScanner interface{ Scan(...any) error }

func scanPlan(row rowScanner) (Plan, error) {
	var plan Plan
	var specification, policy []byte
	var hasChanges sql.NullBool
	var jsonPath, textPath, jsonChecksum, textChecksum sql.NullString
	var artifactCreatedAt, approvedAt, leaseExpiresAt sql.NullTime
	var approvedBy, claimedBy sql.NullString
	if err := row.Scan(
		&plan.ID, &plan.MigrationID, &plan.Status, &specification, &policy, &hasChanges,
		&jsonPath, &textPath, &jsonChecksum, &textChecksum, &artifactCreatedAt,
		&plan.FailureMessage, &plan.AttemptCount, &approvedBy, &approvedAt,
		&plan.CreatedAt, &plan.UpdatedAt, &plan.IdempotencyKey, &plan.RequestHash, &claimedBy, &leaseExpiresAt,
	); err != nil {
		return Plan{}, err
	}
	if err := json.Unmarshal(specification, &plan.Specification); err != nil {
		return Plan{}, fmt.Errorf("decode plan specification: %w", err)
	}
	if err := json.Unmarshal(policy, &plan.Policy); err != nil {
		return Plan{}, fmt.Errorf("decode plan policy: %w", err)
	}
	if hasChanges.Valid {
		plan.HasChanges = boolPointer(hasChanges.Bool)
	}
	if jsonPath.Valid {
		plan.Artifact = &iac.ArtifactMetadata{
			MigrationID: plan.MigrationID, JSONPath: jsonPath.String, TextPath: textPath.String,
			JSONSHA256: jsonChecksum.String, TextSHA256: textChecksum.String, CreatedAt: artifactCreatedAt.Time,
		}
	}
	if approvedBy.Valid {
		plan.ApprovedBy = approvedBy.String
	}
	if approvedAt.Valid {
		plan.ApprovedAt = timePointer(approvedAt.Time)
	}
	if claimedBy.Valid {
		plan.ClaimedBy = claimedBy.String
	}
	if leaseExpiresAt.Valid {
		plan.LeaseExpiresAt = timePointer(leaseExpiresAt.Time)
	}
	return plan, nil
}

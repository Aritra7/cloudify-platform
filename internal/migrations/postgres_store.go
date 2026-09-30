package migrations

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// PostgresStore persists migrations and serializes lifecycle transitions.
type PostgresStore struct {
	db  *sql.DB
	now func() time.Time
}

// NewPostgresStore constructs a durable migration store.
func NewPostgresStore(db *sql.DB) *PostgresStore {
	return &PostgresStore{
		db:  db,
		now: func() time.Time { return time.Now().UTC() },
	}
}

// CreateOrGet atomically implements idempotent migration creation.
func (s *PostgresStore) CreateOrGet(ctx context.Context, candidate Migration) (Migration, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Migration{}, false, fmt.Errorf("begin create migration transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	row := tx.QueryRowContext(ctx, `
		INSERT INTO migrations (
			id, idempotency_key, request_hash, status,
			source_repository_url, source_revision,
			destination_provider, destination_project_id, destination_region,
			destination_runtime, destination_database, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		ON CONFLICT (idempotency_key) DO NOTHING
		RETURNING `+migrationColumns,
		candidate.ID,
		candidate.IdempotencyKey,
		candidate.RequestHash,
		candidate.Status,
		candidate.Source.RepositoryURL,
		candidate.Source.Revision,
		candidate.Destination.Provider,
		candidate.Destination.ProjectID,
		candidate.Destination.Region,
		candidate.Destination.Runtime,
		candidate.Destination.Database,
		candidate.CreatedAt,
		candidate.UpdatedAt,
	)

	created, err := scanMigration(row)
	if err == nil {
		if err := tx.Commit(); err != nil {
			return Migration{}, false, fmt.Errorf("commit created migration: %w", err)
		}
		return created, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Migration{}, false, fmt.Errorf("insert migration: %w", err)
	}

	existing, err := scanMigration(tx.QueryRowContext(
		ctx,
		`SELECT `+migrationColumns+` FROM migrations WHERE idempotency_key = $1`,
		candidate.IdempotencyKey,
	))
	if err != nil {
		return Migration{}, false, fmt.Errorf("read idempotent migration: %w", err)
	}
	if existing.RequestHash != candidate.RequestHash {
		return Migration{}, false, ErrIdempotencyConflict
	}
	if err := tx.Commit(); err != nil {
		return Migration{}, false, fmt.Errorf("commit idempotent migration: %w", err)
	}
	return existing, false, nil
}

// Get returns a migration by ID.
func (s *PostgresStore) Get(ctx context.Context, id string) (Migration, error) {
	migration, err := scanMigration(s.db.QueryRowContext(
		ctx,
		`SELECT `+migrationColumns+` FROM migrations WHERE id = $1`,
		id,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return Migration{}, ErrNotFound
	}
	if err != nil {
		return Migration{}, fmt.Errorf("read migration: %w", err)
	}
	return migration, nil
}

// Transition locks a migration and applies one valid lifecycle transition.
func (s *PostgresStore) Transition(ctx context.Context, id string, to Status) (Migration, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Migration{}, fmt.Errorf("begin transition transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	current, err := scanMigration(tx.QueryRowContext(
		ctx,
		`SELECT `+migrationColumns+` FROM migrations WHERE id = $1 FOR UPDATE`,
		id,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return Migration{}, ErrNotFound
	}
	if err != nil {
		return Migration{}, fmt.Errorf("lock migration: %w", err)
	}
	if !CanTransition(current.Status, to) {
		return Migration{}, ErrInvalidTransition
	}

	updated, err := scanMigration(tx.QueryRowContext(
		ctx,
		`UPDATE migrations SET status = $2, updated_at = $3 WHERE id = $1 RETURNING `+migrationColumns,
		id,
		to,
		s.now(),
	))
	if err != nil {
		return Migration{}, fmt.Errorf("update migration state: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Migration{}, fmt.Errorf("commit migration transition: %w", err)
	}
	return updated, nil
}

// ClaimNext uses a row lock with SKIP LOCKED so multiple dispatcher replicas
// can safely compete for queued work. Expired running leases are reclaimable.
func (s *PostgresStore) ClaimNext(
	ctx context.Context,
	workerID string,
	now time.Time,
	leaseUntil time.Time,
) (Migration, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Migration{}, false, fmt.Errorf("begin claim transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	migration, err := scanMigration(tx.QueryRowContext(ctx, `
		WITH candidate AS (
			SELECT id
			FROM migrations
			WHERE status = 'queued'
			   OR (status = 'running' AND lease_expires_at <= $2)
			ORDER BY created_at
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		UPDATE migrations AS migration
		SET status = 'running', claimed_by = $1, lease_expires_at = $3,
		    updated_at = $2, attempt_count = migration.attempt_count + 1
		FROM candidate
		WHERE migration.id = candidate.id
		RETURNING `+qualifiedMigrationColumns("migration"),
		workerID,
		now,
		leaseUntil,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return Migration{}, false, nil
	}
	if err != nil {
		return Migration{}, false, fmt.Errorf("claim migration: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE migration_attempts
		SET status = 'failed', finished_at = $2, heartbeat_at = $2
		WHERE migration_id = $1 AND finished_at IS NULL`, migration.ID, now); err != nil {
		return Migration{}, false, fmt.Errorf("close expired migration attempt: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO migration_attempts (
			migration_id, attempt_number, worker_id, status, started_at, heartbeat_at
		) VALUES ($1, $2, $3, 'running', $4, $4)`,
		migration.ID, migration.AttemptCount, workerID, now,
	); err != nil {
		return Migration{}, false, fmt.Errorf("create migration attempt: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Migration{}, false, fmt.Errorf("commit migration claim: %w", err)
	}
	return migration, true, nil
}

// RenewLease extends an active lease only for its current owner.
func (s *PostgresStore) RenewLease(
	ctx context.Context,
	id string,
	workerID string,
	now time.Time,
	leaseUntil time.Time,
) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin lease renewal transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `
		UPDATE migrations
		SET lease_expires_at = $4, updated_at = $3
		WHERE id = $1
		  AND claimed_by = $2
		  AND lease_expires_at > $3
		  AND status IN ('running', 'cancelling')`,
		id,
		workerID,
		now,
		leaseUntil,
	)
	if err != nil {
		return fmt.Errorf("renew migration lease: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read renewed lease count: %w", err)
	}
	if rows != 1 {
		return ErrLeaseLost
	}
	result, err = tx.ExecContext(ctx, `
		UPDATE migration_attempts
		SET heartbeat_at = $3
		WHERE migration_id = $1 AND worker_id = $2 AND finished_at IS NULL`, id, workerID, now)
	if err != nil {
		return fmt.Errorf("update migration attempt heartbeat: %w", err)
	}
	rows, err = result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read attempt heartbeat count: %w", err)
	}
	if rows != 1 {
		return ErrLeaseLost
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit lease renewal: %w", err)
	}
	return nil
}

// Complete records a terminal state only when the caller still owns the job.
func (s *PostgresStore) Complete(
	ctx context.Context,
	id string,
	workerID string,
	to Status,
	now time.Time,
) (Migration, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Migration{}, fmt.Errorf("begin completion transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	current, err := scanMigration(tx.QueryRowContext(
		ctx,
		`SELECT `+migrationColumns+` FROM migrations WHERE id = $1 FOR UPDATE`,
		id,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return Migration{}, ErrNotFound
	}
	if err != nil {
		return Migration{}, fmt.Errorf("lock migration for completion: %w", err)
	}
	if current.ClaimedBy != workerID {
		return Migration{}, ErrLeaseLost
	}
	if !CanTransition(current.Status, to) {
		return Migration{}, ErrInvalidTransition
	}

	completed, err := scanMigration(tx.QueryRowContext(ctx, `
		UPDATE migrations
		SET status = $3, claimed_by = NULL, lease_expires_at = NULL, updated_at = $4
		WHERE id = $1 AND claimed_by = $2
		RETURNING `+migrationColumns,
		id,
		workerID,
		to,
		now,
	))
	if err != nil {
		return Migration{}, fmt.Errorf("complete migration: %w", err)
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE migration_attempts
		SET status = $3, heartbeat_at = $4, finished_at = $4
		WHERE migration_id = $1 AND worker_id = $2 AND finished_at IS NULL`,
		id, workerID, to, now,
	)
	if err != nil {
		return Migration{}, fmt.Errorf("complete migration attempt: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return Migration{}, fmt.Errorf("read completed attempt count: %w", err)
	}
	if rows != 1 {
		return Migration{}, ErrLeaseLost
	}
	if err := tx.Commit(); err != nil {
		return Migration{}, fmt.Errorf("commit migration completion: %w", err)
	}
	return completed, nil
}

// Retry atomically requeues a failed migration without deleting attempt history.
func (s *PostgresStore) Retry(ctx context.Context, id string, now time.Time) (Migration, error) {
	migration, err := scanMigration(s.db.QueryRowContext(ctx, `
		UPDATE migrations
		SET status = 'queued', updated_at = $2
		WHERE id = $1 AND status = 'failed'
		RETURNING `+migrationColumns, id, now))
	if errors.Is(err, sql.ErrNoRows) {
		if _, getErr := s.Get(ctx, id); errors.Is(getErr, ErrNotFound) {
			return Migration{}, ErrNotFound
		} else if getErr != nil {
			return Migration{}, getErr
		}
		return Migration{}, ErrInvalidTransition
	}
	if err != nil {
		return Migration{}, fmt.Errorf("retry migration: %w", err)
	}
	return migration, nil
}

// ListAttempts returns immutable execution history in attempt-number order.
func (s *PostgresStore) ListAttempts(ctx context.Context, id string) ([]Attempt, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, migration_id::text, attempt_number, worker_id, status,
		       started_at, heartbeat_at, finished_at
		FROM migration_attempts
		WHERE migration_id = $1
		ORDER BY attempt_number`, id)
	if err != nil {
		return nil, fmt.Errorf("list migration attempts: %w", err)
	}
	defer rows.Close()
	attempts := make([]Attempt, 0)
	for rows.Next() {
		var attempt Attempt
		var finishedAt sql.NullTime
		if err := rows.Scan(
			&attempt.ID, &attempt.MigrationID, &attempt.Number, &attempt.WorkerID,
			&attempt.Status, &attempt.StartedAt, &attempt.HeartbeatAt, &finishedAt,
		); err != nil {
			return nil, fmt.Errorf("scan migration attempt: %w", err)
		}
		if finishedAt.Valid {
			attempt.FinishedAt = &finishedAt.Time
		}
		attempts = append(attempts, attempt)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate migration attempts: %w", err)
	}
	return attempts, nil
}

const migrationColumns = `
	id::text, status, source_repository_url, source_revision,
	destination_provider, destination_project_id, destination_region,
	destination_runtime, destination_database, created_at, updated_at,
	idempotency_key, request_hash, claimed_by, lease_expires_at, attempt_count`

func qualifiedMigrationColumns(alias string) string {
	return fmt.Sprintf(`
	%s.id::text, %s.status, %s.source_repository_url, %s.source_revision,
	%s.destination_provider, %s.destination_project_id, %s.destination_region,
	%s.destination_runtime, %s.destination_database, %s.created_at, %s.updated_at,
	%s.idempotency_key, %s.request_hash, %s.claimed_by, %s.lease_expires_at, %s.attempt_count`,
		alias, alias, alias, alias, alias,
		alias, alias, alias,
		alias, alias, alias, alias,
		alias, alias, alias, alias,
	)
}

type rowScanner interface {
	Scan(...any) error
}

func scanMigration(row rowScanner) (Migration, error) {
	var migration Migration
	var claimedBy sql.NullString
	var leaseExpiresAt sql.NullTime
	err := row.Scan(
		&migration.ID,
		&migration.Status,
		&migration.Source.RepositoryURL,
		&migration.Source.Revision,
		&migration.Destination.Provider,
		&migration.Destination.ProjectID,
		&migration.Destination.Region,
		&migration.Destination.Runtime,
		&migration.Destination.Database,
		&migration.CreatedAt,
		&migration.UpdatedAt,
		&migration.IdempotencyKey,
		&migration.RequestHash,
		&claimedBy,
		&leaseExpiresAt,
		&migration.AttemptCount,
	)
	if claimedBy.Valid {
		migration.ClaimedBy = claimedBy.String
	}
	if leaseExpiresAt.Valid {
		migration.LeaseExpiresAt = &leaseExpiresAt.Time
	}
	return migration, err
}

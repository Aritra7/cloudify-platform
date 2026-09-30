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
	migration, err := scanMigration(s.db.QueryRowContext(ctx, `
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
		SET status = 'running', claimed_by = $1, lease_expires_at = $3, updated_at = $2
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
	result, err := s.db.ExecContext(ctx, `
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
	if err := tx.Commit(); err != nil {
		return Migration{}, fmt.Errorf("commit migration completion: %w", err)
	}
	return completed, nil
}

const migrationColumns = `
	id::text, status, source_repository_url, source_revision,
	destination_provider, destination_project_id, destination_region,
	destination_runtime, destination_database, created_at, updated_at,
	idempotency_key, request_hash, claimed_by, lease_expires_at`

func qualifiedMigrationColumns(alias string) string {
	return fmt.Sprintf(`
	%s.id::text, %s.status, %s.source_repository_url, %s.source_revision,
	%s.destination_provider, %s.destination_project_id, %s.destination_region,
	%s.destination_runtime, %s.destination_database, %s.created_at, %s.updated_at,
	%s.idempotency_key, %s.request_hash, %s.claimed_by, %s.lease_expires_at`,
		alias, alias, alias, alias,
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
	)
	if claimedBy.Valid {
		migration.ClaimedBy = claimedBy.String
	}
	if leaseExpiresAt.Valid {
		migration.LeaseExpiresAt = &leaseExpiresAt.Time
	}
	return migration, err
}

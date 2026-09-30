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

const migrationColumns = `
	id::text, status, source_repository_url, source_revision,
	destination_provider, destination_project_id, destination_region,
	destination_runtime, destination_database, created_at, updated_at,
	idempotency_key, request_hash`

type rowScanner interface {
	Scan(...any) error
}

func scanMigration(row rowScanner) (Migration, error) {
	var migration Migration
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
	)
	return migration, err
}

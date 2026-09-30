package events

import (
	"context"
	"database/sql"
	"fmt"
)

// PostgresStore persists ordered migration events.
type PostgresStore struct {
	db *sql.DB
}

// NewPostgresStore constructs a durable event store.
func NewPostgresStore(db *sql.DB) *PostgresStore {
	return &PostgresStore{db: db}
}

func (store *PostgresStore) Append(ctx context.Context, event Event) (Event, error) {
	err := store.db.QueryRowContext(ctx, `
		INSERT INTO migration_events (
			migration_id, kind, phase, stream, message, created_at
		) VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING sequence`,
		event.MigrationID,
		event.Kind,
		event.Phase,
		event.Stream,
		event.Message,
		event.CreatedAt,
	).Scan(&event.Sequence)
	if err != nil {
		return Event{}, fmt.Errorf("append migration event: %w", err)
	}
	return event, nil
}

func (store *PostgresStore) ListAfter(
	ctx context.Context,
	migrationID string,
	after int64,
	limit int,
) ([]Event, error) {
	rows, err := store.db.QueryContext(ctx, `
		SELECT sequence, migration_id::text, kind, phase, stream, message, created_at
		FROM migration_events
		WHERE migration_id = $1 AND sequence > $2
		ORDER BY sequence
		LIMIT $3`,
		migrationID,
		after,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list migration events: %w", err)
	}
	defer rows.Close()

	result := make([]Event, 0, limit)
	for rows.Next() {
		var event Event
		if err := rows.Scan(
			&event.Sequence,
			&event.MigrationID,
			&event.Kind,
			&event.Phase,
			&event.Stream,
			&event.Message,
			&event.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan migration event: %w", err)
		}
		result = append(result, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate migration events: %w", err)
	}
	return result, nil
}

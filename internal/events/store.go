package events

import (
	"context"
	"time"
)

// Event is an ordered, durable fact associated with one migration.
type Event struct {
	Sequence    int64     `json:"sequence"`
	MigrationID string    `json:"migration_id"`
	Kind        string    `json:"kind"`
	Phase       string    `json:"phase,omitempty"`
	Stream      string    `json:"stream,omitempty"`
	Message     string    `json:"message"`
	CreatedAt   time.Time `json:"created_at"`
}

// Store persists and reads migration events in sequence order.
type Store interface {
	Append(context.Context, Event) (Event, error)
	ListAfter(context.Context, string, int64, int) ([]Event, error)
}

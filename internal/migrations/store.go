package migrations

import "context"

// Store defines the concurrency boundary for persistent migration state.
type Store interface {
	CreateOrGet(context.Context, Migration) (migration Migration, created bool, err error)
	Get(context.Context, string) (Migration, error)
	Transition(context.Context, string, Status) (Migration, error)
}

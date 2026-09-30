package migrations

import "context"
import "time"

// Store defines the concurrency boundary for persistent migration state.
type Store interface {
	CreateOrGet(context.Context, Migration) (migration Migration, created bool, err error)
	Get(context.Context, string) (Migration, error)
	Transition(context.Context, string, Status) (Migration, error)
	ClaimNext(context.Context, string, time.Time, time.Time) (migration Migration, claimed bool, err error)
	RenewLease(context.Context, string, string, time.Time, time.Time) error
	Complete(context.Context, string, string, Status, time.Time) (Migration, error)
}

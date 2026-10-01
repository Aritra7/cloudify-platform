package resources

import (
	"context"
	"time"

	"github.com/Aritra7/cloudify-platform/internal/plans"
)

type Store interface {
	UpsertApplied(context.Context, Resource) (Resource, bool, error)
	Get(context.Context, string) (Resource, error)
	List(context.Context, int) ([]Resource, error)
	RequestDeletion(context.Context, string, string, time.Time) (Resource, error)
	ClaimNext(context.Context, string, time.Time, time.Time) (Resource, bool, error)
	RenewLease(context.Context, string, string, time.Time, time.Time) error
	Complete(context.Context, string, string, ReconcileResult, time.Time) (Resource, error)
	CompleteDeletion(context.Context, string, string, string, time.Time, time.Time) (Resource, error)
	ListEvents(context.Context, string, int64, int) ([]Event, error)
}

type AppliedPlanSource interface {
	ListApplied(context.Context, time.Time, string, int) ([]plans.Plan, error)
}

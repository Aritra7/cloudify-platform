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
}

type AppliedPlanSource interface {
	ListApplied(context.Context, time.Time, string, int) ([]plans.Plan, error)
}

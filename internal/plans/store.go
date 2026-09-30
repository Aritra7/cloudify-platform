package plans

import (
	"context"
	"time"

	"github.com/Aritra7/cloudify-platform/internal/iac"
)

type Store interface {
	CreateOrGet(context.Context, Plan) (Plan, bool, error)
	Get(context.Context, string) (Plan, error)
	ClaimNext(context.Context, string, time.Time, time.Time) (Plan, bool, error)
	RenewLease(context.Context, string, string, time.Time, time.Time) error
	Complete(context.Context, string, string, Status, bool, *iac.ArtifactMetadata, string, time.Time) (Plan, error)
	Approve(context.Context, string, string, time.Time) (Plan, error)
	QueueApply(context.Context, string, string, time.Time) (Plan, error)
	ClaimNextApply(context.Context, string, time.Time, time.Time) (Plan, bool, error)
	CompleteApply(context.Context, string, string, Status, string, time.Time) (Plan, error)
	ListApplied(context.Context, time.Time, string, int) ([]Plan, error)
}

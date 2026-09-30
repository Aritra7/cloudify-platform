package resources

import (
	"context"
	"errors"
	"time"

	"github.com/Aritra7/cloudify-platform/internal/plans"
)

type Service struct {
	store Store
	now   func() time.Time
}

func NewService(store Store) *Service {
	return &Service{store: store, now: func() time.Time { return time.Now().UTC() }}
}

func (service *Service) ProjectApplied(ctx context.Context, plan plans.Plan) (Resource, bool, error) {
	if plan.Status != plans.StatusApplied {
		return Resource{}, false, errors.New("only an applied Terraform plan can become a managed resource")
	}
	now := service.now()
	candidate := Resource{
		ID: appliedResourceID(plan.Specification), Kind: "cloud_run_service",
		MigrationID: plan.MigrationID, SourcePlanID: plan.ID,
		ProjectID: plan.Specification.ProjectID, Region: plan.Specification.Region, Name: plan.Specification.ServiceName,
		Desired: plan.Specification, State: StateUnknown, RemediationPolicy: RemediationAutomatic,
		Generation: 1, Conditions: []Condition{}, NextReconcileAt: now, CreatedAt: now, UpdatedAt: now,
	}
	return service.store.UpsertApplied(ctx, candidate)
}

func (service *Service) Get(ctx context.Context, id string) (Resource, error) {
	return service.store.Get(ctx, id)
}

func (service *Service) List(ctx context.Context, limit int) ([]Resource, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	return service.store.List(ctx, limit)
}

package resources

import (
	"context"
	"testing"
	"time"

	"github.com/Aritra7/cloudify-platform/internal/iac"
	"github.com/Aritra7/cloudify-platform/internal/plans"
)

func TestAppliedPlansProjectIdempotentlyAndAdvanceGeneration(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore()
	service := NewService(store)
	service.now = func() time.Time { return time.Unix(100, 0).UTC() }
	first := appliedPlan("plan-1", "image-one")
	resource, changed, err := service.ProjectApplied(context.Background(), first)
	if err != nil || !changed || resource.Generation != 1 || resource.State != StateUnknown {
		t.Fatalf("first projection = (%#v, %v, %v)", resource, changed, err)
	}
	replayed, changed, err := service.ProjectApplied(context.Background(), first)
	if err != nil || changed || replayed.Generation != 1 {
		t.Fatalf("replayed projection = (%#v, %v, %v)", replayed, changed, err)
	}
	sameDesired := first
	sameDesired.ID = "remediation-plan"
	remediated, changed, err := service.ProjectApplied(context.Background(), sameDesired)
	if err != nil || !changed || remediated.Generation != 1 {
		t.Fatalf("remediation projection = (%#v, %v, %v)", remediated, changed, err)
	}
	second := appliedPlan("plan-2", "image-two")
	updated, changed, err := service.ProjectApplied(context.Background(), second)
	if err != nil || !changed || updated.ID != resource.ID || updated.Generation != 2 || updated.SourcePlanID != "plan-2" {
		t.Fatalf("updated projection = (%#v, %v, %v)", updated, changed, err)
	}
}

func TestProjectorUsesStableCursor(t *testing.T) {
	t.Parallel()
	first := appliedPlan("plan-1", "image-one")
	first.UpdatedAt = time.Unix(100, 0).UTC()
	second := appliedPlan("plan-2", "image-two")
	second.Specification.ServiceName = "second-api"
	second.UpdatedAt = time.Unix(101, 0).UTC()
	source := &planSource{plans: []plans.Plan{first, second}}
	service := NewService(NewMemoryStore())
	projector := &Projector{Plans: source, Resources: service, PollInterval: time.Second, BatchSize: 1}
	if processed, err := projector.RunOnce(context.Background()); err != nil || !processed {
		t.Fatalf("first RunOnce = (%v, %v)", processed, err)
	}
	if processed, err := projector.RunOnce(context.Background()); err != nil || !processed {
		t.Fatalf("second RunOnce = (%v, %v)", processed, err)
	}
	listed, err := service.List(context.Background(), 100)
	if err != nil || len(listed) != 2 {
		t.Fatalf("resources = (%#v, %v)", listed, err)
	}
}

type planSource struct{ plans []plans.Plan }

func (source *planSource) ListApplied(_ context.Context, after time.Time, afterID string, limit int) ([]plans.Plan, error) {
	result := make([]plans.Plan, 0, limit)
	for _, plan := range source.plans {
		if plan.UpdatedAt.After(after) || (plan.UpdatedAt.Equal(after) && plan.ID > afterID) {
			result = append(result, plan)
			if len(result) == limit {
				break
			}
		}
	}
	return result, nil
}

func appliedPlan(id, imageLabel string) plans.Plan {
	imageDigest := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if imageLabel == "image-two" {
		imageDigest = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	}
	return plans.Plan{
		ID: id, MigrationID: "7b629d1d-7602-4de6-82bd-340fc18e55b6", Status: plans.StatusApplied,
		Specification: iac.DeploymentSpec{
			Version: iac.SpecificationVersion, MigrationID: "7b629d1d-7602-4de6-82bd-340fc18e55b6",
			ProjectID: "example-project", Region: "us-central1", ServiceName: "example-api",
			Image:               "us-docker.pkg.dev/example-project/apps/api@sha256:" + imageDigest,
			ServiceAccountEmail: "cloud-run@example-project.iam.gserviceaccount.com",
			CPU:                 "1", Memory: "512Mi", MaxInstances: 3,
		},
		UpdatedAt: time.Unix(100, 0).UTC(),
	}
}

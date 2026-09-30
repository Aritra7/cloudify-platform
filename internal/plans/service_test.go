package plans

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Aritra7/cloudify-platform/internal/iac"
)

func TestPlanCreationIsIdempotentAndPolicyGated(t *testing.T) {
	t.Parallel()
	service := NewService(NewMemoryStore(), DefaultPolicy())
	spec := validSpec()
	first, created, err := service.Create(context.Background(), spec.MigrationID, "request-1", spec)
	if err != nil || !created || first.Status != StatusQueued || !first.Policy.Allowed {
		t.Fatalf("first create = (%#v, %v, %v)", first, created, err)
	}
	replayed, created, err := service.Create(context.Background(), spec.MigrationID, "request-1", spec)
	if err != nil || created || replayed.ID != first.ID {
		t.Fatalf("replay = (%#v, %v, %v)", replayed, created, err)
	}
	spec.MaxInstances = 20
	if _, _, err := service.Create(context.Background(), spec.MigrationID, "request-1", spec); !errors.Is(err, ErrIdempotency) {
		t.Fatalf("conflicting replay error = %v, want ErrIdempotency", err)
	}

	rejectedSpec := validSpec()
	rejectedSpec.AllowUnauthenticated = true
	rejected, created, err := service.Create(context.Background(), rejectedSpec.MigrationID, "request-2", rejectedSpec)
	if err != nil || !created || rejected.Status != StatusRejected || rejected.Policy.Allowed {
		t.Fatalf("rejected create = (%#v, %v, %v)", rejected, created, err)
	}
}

func TestPlanApprovalRequiresReadyArtifactAndActor(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore()
	service := NewService(store, DefaultPolicy())
	spec := validSpec()
	plan, _, err := service.Create(context.Background(), spec.MigrationID, "request-1", spec)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := service.Approve(context.Background(), plan.ID, "reviewer@example.com"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("early approval error = %v", err)
	}
	now := time.Unix(100, 0).UTC()
	if _, claimed, err := store.ClaimNext(context.Background(), "worker-1", now, now.Add(time.Minute)); err != nil || !claimed {
		t.Fatalf("claim = (%v, %v)", claimed, err)
	}
	artifactValue := testArtifact(spec.MigrationID)
	artifactValue.CreatedAt = now
	artifact := &artifactValue
	if _, err := store.Complete(context.Background(), plan.ID, "worker-1", StatusReady, true, artifact, "", now); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if _, err := service.Approve(context.Background(), plan.ID, ""); err == nil {
		t.Fatal("approval without actor returned nil")
	}
	approved, err := service.Approve(context.Background(), plan.ID, "reviewer@example.com")
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if approved.Status != StatusApproved || approved.ApprovedBy != "reviewer@example.com" || approved.ApprovedAt == nil {
		t.Fatalf("approved plan = %#v", approved)
	}
}

func validSpec() iac.DeploymentSpec {
	return iac.DeploymentSpec{
		Version: iac.SpecificationVersion, MigrationID: "7b629d1d-7602-4de6-82bd-340fc18e55b6",
		ProjectID: "example-project", Region: "us-central1", ServiceName: "example-api",
		Image:               "us-docker.pkg.dev/example-project/apps/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ServiceAccountEmail: "cloud-run@example-project.iam.gserviceaccount.com",
		CPU:                 "1", Memory: "512Mi", MinInstances: 0, MaxInstances: 3,
	}
}

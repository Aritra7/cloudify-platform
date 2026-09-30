package plans

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Aritra7/cloudify-platform/internal/iac"
)

type plannerFunc func(context.Context, string, iac.DeploymentSpec) (iac.PlanResult, error)

func (function plannerFunc) Plan(ctx context.Context, workspace string, spec iac.DeploymentSpec) (iac.PlanResult, error) {
	return function(ctx, workspace, spec)
}

func TestDispatcherStoresReadyPlan(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore()
	service := NewService(store, DefaultPolicy())
	spec := validSpec()
	created, _, err := service.Create(context.Background(), spec.MigrationID, "request-1", spec)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	dispatcher := testDispatcher(t, store, plannerFunc(func(context.Context, string, iac.DeploymentSpec) (iac.PlanResult, error) {
		return iac.PlanResult{HasChanges: true, Artifact: testArtifact(spec.MigrationID)}, nil
	}))
	if processed, err := dispatcher.RunOnce(context.Background()); err != nil || !processed {
		t.Fatalf("RunOnce = (%v, %v)", processed, err)
	}
	stored, err := service.Get(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if stored.Status != StatusReady || stored.Artifact == nil || stored.HasChanges == nil || !*stored.HasChanges {
		t.Fatalf("stored plan = %#v", stored)
	}
}

func testArtifact(migrationID string) iac.ArtifactMetadata {
	return iac.ArtifactMetadata{
		MigrationID: migrationID, JSONPath: "/plan.json", TextPath: "/plan.txt",
		JSONSHA256:      "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		TextSHA256:      "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		BinaryObjectKey: migrationID + "/plan.enc",
		BinarySHA256:    "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
	}
}

func TestDispatcherPersistsPlannerFailure(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore()
	service := NewService(store, DefaultPolicy())
	spec := validSpec()
	created, _, err := service.Create(context.Background(), spec.MigrationID, "request-1", spec)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	dispatcher := testDispatcher(t, store, plannerFunc(func(context.Context, string, iac.DeploymentSpec) (iac.PlanResult, error) {
		return iac.PlanResult{}, errors.New("Terraform plan failed")
	}))
	if processed, err := dispatcher.RunOnce(context.Background()); err != nil || !processed {
		t.Fatalf("RunOnce = (%v, %v)", processed, err)
	}
	stored, err := service.Get(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if stored.Status != StatusFailed || stored.FailureMessage == "" {
		t.Fatalf("stored plan = %#v", stored)
	}
}

func testDispatcher(t *testing.T, store Store, planner Planner) *Dispatcher {
	t.Helper()
	return &Dispatcher{
		Store: store, Planner: planner, WorkerID: "terraform-worker-1", WorkRoot: t.TempDir(),
		LeaseDuration: time.Minute, HeartbeatInterval: 30 * time.Second, PollInterval: time.Millisecond,
		Now: func() time.Time { return time.Now().UTC() },
	}
}

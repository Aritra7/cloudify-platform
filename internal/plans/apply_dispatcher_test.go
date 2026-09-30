package plans

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Aritra7/cloudify-platform/internal/iac"
)

type applierFunc func(context.Context, string, iac.DeploymentSpec, iac.ArtifactMetadata) error

func (function applierFunc) Apply(ctx context.Context, workspace string, spec iac.DeploymentSpec, artifact iac.ArtifactMetadata) error {
	return function(ctx, workspace, spec, artifact)
}

func TestApplyDispatcherCompletesExactApprovedPlan(t *testing.T) {
	t.Parallel()
	store, service, plan := queuedApplyPlan(t)
	dispatcher := testApplyDispatcher(t, store, applierFunc(func(_ context.Context, _ string, spec iac.DeploymentSpec, artifact iac.ArtifactMetadata) error {
		if spec.MigrationID != artifact.MigrationID {
			return errors.New("artifact mismatch")
		}
		return nil
	}))
	if processed, err := dispatcher.RunOnce(context.Background()); err != nil || !processed {
		t.Fatalf("RunOnce = (%v, %v)", processed, err)
	}
	stored, err := service.Get(context.Background(), plan.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if stored.Status != StatusApplied || stored.ApplyAttemptCount != 1 {
		t.Fatalf("stored plan = %#v", stored)
	}
}

func TestApplyDispatcherPersistsSafeFailure(t *testing.T) {
	t.Parallel()
	store, service, plan := queuedApplyPlan(t)
	dispatcher := testApplyDispatcher(t, store, applierFunc(func(context.Context, string, iac.DeploymentSpec, iac.ArtifactMetadata) error {
		return errors.New("provider output containing secret material")
	}))
	if processed, err := dispatcher.RunOnce(context.Background()); err != nil || !processed {
		t.Fatalf("RunOnce = (%v, %v)", processed, err)
	}
	stored, err := service.Get(context.Background(), plan.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if stored.Status != StatusApplyFailed || stored.FailureMessage != "Terraform apply failed" {
		t.Fatalf("stored plan = %#v", stored)
	}
}

func queuedApplyPlan(t *testing.T) (*MemoryStore, *Service, Plan) {
	t.Helper()
	store := NewMemoryStore()
	service := NewService(store, DefaultPolicy())
	spec := validSpec()
	plan, _, err := service.Create(context.Background(), spec.MigrationID, t.Name(), spec)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	now := time.Unix(100, 0).UTC()
	if _, claimed, err := store.ClaimNext(context.Background(), "plan-worker", now, now.Add(time.Minute)); err != nil || !claimed {
		t.Fatalf("claim plan = (%v, %v)", claimed, err)
	}
	artifact := testArtifact(spec.MigrationID)
	if _, err := store.Complete(context.Background(), plan.ID, "plan-worker", StatusReady, true, &artifact, "", now); err != nil {
		t.Fatalf("complete plan: %v", err)
	}
	if _, err := service.Approve(context.Background(), plan.ID, "approver@example.com"); err != nil {
		t.Fatalf("approve plan: %v", err)
	}
	queued, err := service.QueueApply(context.Background(), plan.ID, "operator@example.com")
	if err != nil {
		t.Fatalf("queue apply: %v", err)
	}
	return store, service, queued
}

func testApplyDispatcher(t *testing.T, store Store, applier Applier) *ApplyDispatcher {
	t.Helper()
	return &ApplyDispatcher{
		Store: store, Applier: applier, WorkerID: "apply-worker", WorkRoot: t.TempDir(),
		LeaseDuration: time.Minute, HeartbeatInterval: 30 * time.Second, PollInterval: time.Millisecond,
		Now: func() time.Time { return time.Now().UTC() },
	}
}

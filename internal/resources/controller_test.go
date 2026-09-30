package resources

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Aritra7/cloudify-platform/internal/iac"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type observerFunc func(context.Context, Resource) (Observation, error)

func (function observerFunc) Observe(ctx context.Context, resource Resource) (Observation, error) {
	return function(ctx, resource)
}

type remediatorFunc func(context.Context, Resource, Observation) error

func (function remediatorFunc) Remediate(ctx context.Context, resource Resource, observed Observation) error {
	return function(ctx, resource, observed)
}

func TestControllerRecordsInSyncObservation(t *testing.T) {
	t.Parallel()
	store, resource := projectedResource(t)
	controller := testController(store, observerFunc(func(context.Context, Resource) (Observation, error) {
		return matchingObservation(resource), nil
	}), nil)
	if processed, err := controller.RunOnce(context.Background()); err != nil || !processed {
		t.Fatalf("RunOnce = (%v, %v)", processed, err)
	}
	stored, err := store.Get(context.Background(), resource.ID)
	if err != nil {
		t.Fatalf("get resource: %v", err)
	}
	if stored.State != StateInSync || stored.ObservedGeneration != stored.Generation || stored.RetryCount != 0 || stored.ClaimedBy != "" {
		t.Fatalf("stored resource = %#v", stored)
	}
}

func TestControllerDetectsDeletionAndTriggersRemediation(t *testing.T) {
	t.Parallel()
	store, resource := projectedResource(t)
	remediated := false
	controller := testController(store, observerFunc(func(context.Context, Resource) (Observation, error) {
		return Observation{Exists: false}, nil
	}), remediatorFunc(func(_ context.Context, got Resource, _ Observation) error {
		remediated = got.ID == resource.ID
		return ErrRemediationPending
	}))
	if processed, err := controller.RunOnce(context.Background()); err != nil || !processed {
		t.Fatalf("RunOnce = (%v, %v)", processed, err)
	}
	stored, _ := store.Get(context.Background(), resource.ID)
	if !remediated || stored.State != StateMissing || len(stored.Conditions) != 2 || stored.Conditions[1].Reason != "TerraformPending" {
		t.Fatalf("stored resource = %#v, remediated=%v", stored, remediated)
	}
}

func TestControllerBacksOffTransientObservationFailure(t *testing.T) {
	t.Parallel()
	store, resource := projectedResource(t)
	controller := testController(store, observerFunc(func(context.Context, Resource) (Observation, error) {
		return Observation{}, status.Error(codes.ResourceExhausted, "quota exceeded")
	}), nil)
	if processed, err := controller.RunOnce(context.Background()); err != nil || !processed {
		t.Fatalf("RunOnce = (%v, %v)", processed, err)
	}
	stored, _ := store.Get(context.Background(), resource.ID)
	if stored.State != StateError || stored.RetryCount != 1 || !stored.NextReconcileAt.After(controller.Now()) || stored.Conditions[0].Reason != "TransientObservationFailure" {
		t.Fatalf("stored resource = %#v", stored)
	}
}

func TestControllerStopsRetryingPermanentObservationFailure(t *testing.T) {
	t.Parallel()
	store, resource := projectedResource(t)
	controller := testController(store, observerFunc(func(context.Context, Resource) (Observation, error) {
		return Observation{}, errors.New("invalid credentials")
	}), nil)
	if processed, err := controller.RunOnce(context.Background()); err != nil || !processed {
		t.Fatalf("RunOnce = (%v, %v)", processed, err)
	}
	stored, _ := store.Get(context.Background(), resource.ID)
	if stored.NextReconcileAt.Year() != 9999 || stored.Conditions[0].Reason != "PermanentObservationFailure" {
		t.Fatalf("stored resource = %#v", stored)
	}
}

func TestExpiredLeaseRejectsStaleControllerCompletion(t *testing.T) {
	t.Parallel()
	store, resource := projectedResource(t)
	now := time.Unix(200, 0).UTC()
	if _, claimed, err := store.ClaimNext(context.Background(), "worker-1", now, now.Add(time.Minute)); err != nil || !claimed {
		t.Fatalf("first claim = (%v, %v)", claimed, err)
	}
	if _, claimed, err := store.ClaimNext(context.Background(), "worker-2", now.Add(30*time.Second), now.Add(2*time.Minute)); err != nil || claimed {
		t.Fatalf("concurrent claim = (%v, %v)", claimed, err)
	}
	if _, claimed, err := store.ClaimNext(context.Background(), "worker-2", now.Add(time.Minute), now.Add(2*time.Minute)); err != nil || !claimed {
		t.Fatalf("recovery claim = (%v, %v)", claimed, err)
	}
	_, err := store.Complete(context.Background(), resource.ID, "worker-1", ReconcileResult{
		State: StateInSync, NextReconcileAt: now.Add(time.Hour), LastReconciledAt: now,
	}, now.Add(time.Minute))
	if !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale completion error = %v, want ErrLeaseLost", err)
	}
}

func projectedResource(t *testing.T) (*MemoryStore, Resource) {
	t.Helper()
	store := NewMemoryStore()
	service := NewService(store)
	now := time.Unix(100, 0).UTC()
	service.now = func() time.Time { return now }
	resource, _, err := service.ProjectApplied(context.Background(), appliedPlan("plan-1", "image-one"))
	if err != nil {
		t.Fatalf("project plan: %v", err)
	}
	return store, resource
}

func testController(store Store, observer Observer, remediator Remediator) *Controller {
	now := time.Unix(200, 0).UTC()
	return &Controller{
		Store: store, Observer: observer, Remediator: remediator, WorkerID: "reconciler-1",
		LeaseDuration: time.Minute, HeartbeatInterval: 30 * time.Second, PollInterval: time.Second,
		ReconcileInterval: time.Minute, PendingInterval: 5 * time.Second,
		Now: func() time.Time { return now },
	}
}

func matchingObservation(resource Resource) Observation {
	secrets := make(map[string]iac.SecretReference, len(resource.Desired.Secrets))
	for _, secret := range resource.Desired.Secrets {
		secrets[secret.EnvironmentVariable] = secret
	}
	return Observation{
		Exists: true, Image: resource.Desired.Image, ServiceAccountEmail: resource.Desired.ServiceAccountEmail,
		CPU: resource.Desired.CPU, Memory: resource.Desired.Memory,
		MinInstances: resource.Desired.MinInstances, MaxInstances: resource.Desired.MaxInstances,
		AllowUnauthenticated:    resource.Desired.AllowUnauthenticated,
		NonSensitiveEnvironment: normalizedEnvironment(resource.Desired.NonSensitiveEnvironment),
		Secrets:                 secrets, ContainerCount: 1,
	}
}

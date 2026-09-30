package migrations

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCreateIsIdempotent(t *testing.T) {
	t.Parallel()

	service := NewService(NewMemoryStore())
	request := validRequest()

	first, created, err := service.Create(context.Background(), "request-1", request)
	if err != nil || !created {
		t.Fatalf("first create = (%v, %v), want successful creation", created, err)
	}
	second, created, err := service.Create(context.Background(), "request-1", request)
	if err != nil || created {
		t.Fatalf("second create = (%v, %v), want successful replay", created, err)
	}
	if first.ID != second.ID {
		t.Fatalf("replayed ID = %q, want %q", second.ID, first.ID)
	}
}

func TestCreateRejectsReusedKeyWithDifferentRequest(t *testing.T) {
	t.Parallel()

	service := NewService(NewMemoryStore())
	request := validRequest()
	if _, _, err := service.Create(context.Background(), "request-1", request); err != nil {
		t.Fatalf("create: %v", err)
	}

	request.Destination.Region = "us-east1"
	_, _, err := service.Create(context.Background(), "request-1", request)
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("error = %v, want ErrIdempotencyConflict", err)
	}
}

func TestCancelQueuedMigration(t *testing.T) {
	t.Parallel()

	store := NewMemoryStore()
	service := NewService(store)
	service.now = func() time.Time { return time.Unix(100, 0).UTC() }

	migration, _, err := service.Create(context.Background(), "request-1", validRequest())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	cancelled, err := service.Cancel(context.Background(), migration.ID)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if cancelled.Status != StatusCancelled {
		t.Fatalf("status = %q, want %q", cancelled.Status, StatusCancelled)
	}
}

func TestExpiredLeaseCanBeReclaimed(t *testing.T) {
	t.Parallel()

	store := NewMemoryStore()
	service := NewService(store)
	migration, _, err := service.Create(context.Background(), "request-1", validRequest())
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	now := time.Unix(100, 0).UTC()
	if _, claimed, err := store.ClaimNext(context.Background(), "worker-1", now, now.Add(time.Minute)); err != nil || !claimed {
		t.Fatalf("first claim = (%v, %v), want successful claim", claimed, err)
	}
	if _, claimed, err := store.ClaimNext(context.Background(), "worker-2", now.Add(30*time.Second), now.Add(2*time.Minute)); err != nil || claimed {
		t.Fatalf("early reclaim = (%v, %v), want no claim", claimed, err)
	}

	reclaimed, claimed, err := store.ClaimNext(
		context.Background(),
		"worker-2",
		now.Add(time.Minute),
		now.Add(2*time.Minute),
	)
	if err != nil || !claimed {
		t.Fatalf("expired reclaim = (%v, %v), want successful claim", claimed, err)
	}
	if reclaimed.ID != migration.ID || reclaimed.ClaimedBy != "worker-2" {
		t.Fatalf("reclaimed migration = (%q, %q)", reclaimed.ID, reclaimed.ClaimedBy)
	}
	if _, err := store.Complete(context.Background(), migration.ID, "worker-1", StatusSucceeded, now.Add(time.Minute)); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale completion error = %v, want ErrLeaseLost", err)
	}
	completed, err := store.Complete(context.Background(), migration.ID, "worker-2", StatusSucceeded, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("owner completion: %v", err)
	}
	if completed.Status != StatusSucceeded {
		t.Fatalf("status = %q, want %q", completed.Status, StatusSucceeded)
	}
}

func validRequest() CreateRequest {
	return CreateRequest{
		Source: Source{
			RepositoryURL: "https://github.com/example/application",
			Revision:      "main",
		},
		Destination: Destination{
			Provider:  "gcp",
			ProjectID: "example-project",
			Region:    "us-central1",
			Runtime:   "cloud-run",
		},
	}
}

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

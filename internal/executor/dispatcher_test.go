package executor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Aritra7/cloudify-platform/internal/migrations"
)

type workerFunc func(context.Context, migrations.Migration) error

func (function workerFunc) Run(ctx context.Context, migration migrations.Migration) error {
	return function(ctx, migration)
}

func TestDispatcherCompletesSuccessfulMigration(t *testing.T) {
	t.Parallel()

	store, service, migration := queuedMigration(t)
	dispatcher := testDispatcher(store, workerFunc(func(_ context.Context, claimed migrations.Migration) error {
		if claimed.ID != migration.ID {
			return errors.New("dispatcher claimed the wrong migration")
		}
		return nil
	}))

	processed, err := dispatcher.RunOnce(context.Background())
	if err != nil || !processed {
		t.Fatalf("RunOnce = (%v, %v), want processed migration", processed, err)
	}
	stored, err := service.Get(context.Background(), migration.ID)
	if err != nil {
		t.Fatalf("get migration: %v", err)
	}
	if stored.Status != migrations.StatusSucceeded {
		t.Fatalf("status = %q, want %q", stored.Status, migrations.StatusSucceeded)
	}
}

func TestDispatcherRecordsWorkerFailure(t *testing.T) {
	t.Parallel()

	store, service, migration := queuedMigration(t)
	dispatcher := testDispatcher(store, workerFunc(func(context.Context, migrations.Migration) error {
		return errors.New("worker failed")
	}))

	if processed, err := dispatcher.RunOnce(context.Background()); err != nil || !processed {
		t.Fatalf("RunOnce = (%v, %v), want recorded worker failure", processed, err)
	}
	stored, err := service.Get(context.Background(), migration.ID)
	if err != nil {
		t.Fatalf("get migration: %v", err)
	}
	if stored.Status != migrations.StatusFailed {
		t.Fatalf("status = %q, want %q", stored.Status, migrations.StatusFailed)
	}
}

func TestDispatcherHonorsCancellationState(t *testing.T) {
	t.Parallel()

	store, service, migration := queuedMigration(t)
	release := make(chan struct{})
	started := make(chan struct{})
	dispatcher := testDispatcher(store, workerFunc(func(context.Context, migrations.Migration) error {
		close(started)
		<-release
		return nil
	}))

	result := make(chan error, 1)
	go func() {
		_, err := dispatcher.RunOnce(context.Background())
		result <- err
	}()
	<-started
	if _, err := service.Cancel(context.Background(), migration.ID); err != nil {
		t.Fatalf("request cancellation: %v", err)
	}
	close(release)
	if err := <-result; err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	stored, err := service.Get(context.Background(), migration.ID)
	if err != nil {
		t.Fatalf("get migration: %v", err)
	}
	if stored.Status != migrations.StatusCancelled {
		t.Fatalf("status = %q, want %q", stored.Status, migrations.StatusCancelled)
	}
}

func queuedMigration(t *testing.T) (*migrations.MemoryStore, *migrations.Service, migrations.Migration) {
	t.Helper()
	store := migrations.NewMemoryStore()
	service := migrations.NewService(store)
	migration, _, err := service.Create(context.Background(), t.Name(), migrations.CreateRequest{
		Source: migrations.Source{
			RepositoryURL: "https://github.com/example/application",
			Revision:      "main",
		},
		Destination: migrations.Destination{
			Provider:  "gcp",
			ProjectID: "example-project",
			Region:    "us-central1",
			Runtime:   "cloud-run",
		},
	})
	if err != nil {
		t.Fatalf("create migration: %v", err)
	}
	return store, service, migration
}

func testDispatcher(store migrations.Store, worker Worker) *Dispatcher {
	return &Dispatcher{
		Store:             store,
		Worker:            worker,
		WorkerID:          "worker-1",
		LeaseDuration:     time.Minute,
		HeartbeatInterval: 30 * time.Second,
		PollInterval:      time.Millisecond,
		Now:               func() time.Time { return time.Now().UTC() },
	}
}

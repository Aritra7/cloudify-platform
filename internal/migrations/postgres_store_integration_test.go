package migrations

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestPostgresStoreLifecycle(t *testing.T) {
	databaseURL := os.Getenv("CLOUDIFY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("CLOUDIFY_TEST_DATABASE_URL is not set")
	}

	database, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	ctx := context.Background()
	if _, err := database.ExecContext(ctx, "DROP TABLE IF EXISTS migrations CASCADE"); err != nil {
		t.Fatalf("reset schema: %v", err)
	}
	migrationFiles, err := filepath.Glob(filepath.Join("..", "..", "db", "migrations", "*.sql"))
	if err != nil {
		t.Fatalf("list schema migrations: %v", err)
	}
	sort.Strings(migrationFiles)
	for _, migrationFile := range migrationFiles {
		schema, err := os.ReadFile(migrationFile)
		if err != nil {
			t.Fatalf("read schema %s: %v", migrationFile, err)
		}
		if _, err := database.ExecContext(ctx, string(schema)); err != nil {
			t.Fatalf("apply schema %s: %v", migrationFile, err)
		}
	}

	service := NewService(NewPostgresStore(database))
	request := validRequest()
	created, wasCreated, err := service.Create(ctx, "integration-request", request)
	if err != nil || !wasCreated {
		t.Fatalf("create = (%v, %v), want successful creation", wasCreated, err)
	}

	replayed, wasCreated, err := service.Create(ctx, "integration-request", request)
	if err != nil || wasCreated {
		t.Fatalf("replay = (%v, %v), want successful replay", wasCreated, err)
	}
	if replayed.ID != created.ID {
		t.Fatalf("replayed ID = %q, want %q", replayed.ID, created.ID)
	}

	request.Destination.Region = "us-east1"
	if _, _, err := service.Create(ctx, "integration-request", request); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("conflicting create error = %v, want ErrIdempotencyConflict", err)
	}

	store := NewPostgresStore(database)
	now := time.Now().UTC()
	claimed, ok, err := store.ClaimNext(ctx, "worker-1", now, now.Add(time.Minute))
	if err != nil || !ok {
		t.Fatalf("claim = (%v, %v), want successful claim", ok, err)
	}
	if claimed.ID != created.ID || claimed.ClaimedBy != "worker-1" {
		t.Fatalf("claimed migration = (%q, %q), want (%q, worker-1)", claimed.ID, claimed.ClaimedBy, created.ID)
	}
	if _, ok, err := store.ClaimNext(ctx, "worker-2", now, now.Add(time.Minute)); err != nil || ok {
		t.Fatalf("second claim = (%v, %v), want no work", ok, err)
	}
	if err := store.RenewLease(ctx, created.ID, "worker-1", now.Add(time.Second), now.Add(2*time.Minute)); err != nil {
		t.Fatalf("renew lease: %v", err)
	}

	cancelling, err := service.Cancel(ctx, created.ID)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if cancelling.Status != StatusCancelling {
		t.Fatalf("cancelling status = %q, want %q", cancelling.Status, StatusCancelling)
	}
	cancelled, err := store.Complete(ctx, created.ID, "worker-1", StatusCancelled, now.Add(2*time.Second))
	if err != nil {
		t.Fatalf("complete cancellation: %v", err)
	}
	if cancelled.Status != StatusCancelled {
		t.Fatalf("cancelled status = %q, want %q", cancelled.Status, StatusCancelled)
	}

	stored, err := service.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if stored.Status != StatusCancelled {
		t.Fatalf("stored status = %q, want %q", stored.Status, StatusCancelled)
	}
}

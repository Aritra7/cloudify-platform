package migrations

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

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
	schema, err := os.ReadFile(filepath.Join("..", "..", "db", "migrations", "001_create_migrations.sql"))
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	if _, err := database.ExecContext(ctx, string(schema)); err != nil {
		t.Fatalf("apply schema: %v", err)
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

	cancelled, err := service.Cancel(ctx, created.ID)
	if err != nil {
		t.Fatalf("cancel: %v", err)
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

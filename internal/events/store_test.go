package events

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/Aritra7/cloudify-platform/internal/migrations"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestMemoryStoreListsEventsAfterCursor(t *testing.T) {
	t.Parallel()

	store := NewMemoryStore()
	first, err := store.Append(context.Background(), Event{MigrationID: "migration-1", Message: "first"})
	if err != nil {
		t.Fatalf("append first event: %v", err)
	}
	second, err := store.Append(context.Background(), Event{MigrationID: "migration-1", Message: "second"})
	if err != nil {
		t.Fatalf("append second event: %v", err)
	}
	if _, err := store.Append(context.Background(), Event{MigrationID: "migration-2", Message: "other"}); err != nil {
		t.Fatalf("append other event: %v", err)
	}

	result, err := store.ListAfter(context.Background(), "migration-1", first.Sequence, 10)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(result) != 1 || result[0].Sequence != second.Sequence {
		t.Fatalf("events = %#v, want only second event", result)
	}
}

func TestPostgresStoreEvents(t *testing.T) {
	databaseURL := os.Getenv("CLOUDIFY_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("CLOUDIFY_TEST_DATABASE_URL is not set")
	}

	database, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = database.Close() })
	ctx := context.Background()
	if _, err := database.ExecContext(ctx, "SELECT pg_advisory_lock(934857)"); err != nil {
		t.Fatalf("acquire integration-test lock: %v", err)
	}
	t.Cleanup(func() { _, _ = database.ExecContext(context.Background(), "SELECT pg_advisory_unlock(934857)") })
	if _, err := database.ExecContext(ctx, "DROP TABLE IF EXISTS terraform_apply_requests, terraform_plan_approvals, terraform_plans, migration_attempts, migration_events, migrations CASCADE"); err != nil {
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

	migrationService := migrations.NewService(migrations.NewPostgresStore(database))
	migration, _, err := migrationService.Create(ctx, "event-test", migrations.CreateRequest{
		Source: migrations.Source{RepositoryURL: "https://github.com/example/application", Revision: "main"},
		Destination: migrations.Destination{
			Provider: "gcp", ProjectID: "example-project", Region: "us-central1", Runtime: "cloud-run",
		},
	})
	if err != nil {
		t.Fatalf("create migration: %v", err)
	}

	store := NewPostgresStore(database)
	first, err := store.Append(ctx, Event{
		MigrationID: migration.ID, Kind: "worker_output", Message: "first", CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("append first event: %v", err)
	}
	second, err := store.Append(ctx, Event{
		MigrationID: migration.ID, Kind: "worker_output", Message: "second", CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("append second event: %v", err)
	}
	eventList, err := store.ListAfter(ctx, migration.ID, first.Sequence, 10)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(eventList) != 1 || eventList[0].Sequence != second.Sequence {
		t.Fatalf("events = %#v, want only second event", eventList)
	}
}

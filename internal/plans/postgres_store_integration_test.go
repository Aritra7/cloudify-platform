package plans

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/Aritra7/cloudify-platform/internal/iac"
	"github.com/Aritra7/cloudify-platform/internal/migrations"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestPostgresPlanLifecycle(t *testing.T) {
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
	if _, err := database.ExecContext(ctx, "DROP TABLE IF EXISTS terraform_plan_approvals, terraform_plans, migration_attempts, migration_events, migrations CASCADE"); err != nil {
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
			t.Fatalf("read schema: %v", err)
		}
		if _, err := database.ExecContext(ctx, string(schema)); err != nil {
			t.Fatalf("apply schema %s: %v", migrationFile, err)
		}
	}

	migrationStore := migrations.NewPostgresStore(database)
	migrationService := migrations.NewService(migrationStore)
	migration, _, err := migrationService.Create(ctx, "migration-request", migrations.CreateRequest{
		Source:      migrations.Source{RepositoryURL: "https://github.com/example/application", Revision: "main"},
		Destination: migrations.Destination{Provider: "gcp", ProjectID: "example-project", Region: "us-central1", Runtime: "cloud-run"},
	})
	if err != nil {
		t.Fatalf("create migration: %v", err)
	}

	store := NewPostgresStore(database)
	service := NewService(store, DefaultPolicy())
	spec := validSpec()
	created, wasCreated, err := service.Create(ctx, migration.ID, "plan-request", spec)
	if err != nil || !wasCreated {
		t.Fatalf("create plan = (%v, %v)", wasCreated, err)
	}
	replayed, wasCreated, err := service.Create(ctx, migration.ID, "plan-request", spec)
	if err != nil || wasCreated || replayed.ID != created.ID {
		t.Fatalf("replay plan = (%#v, %v, %v)", replayed, wasCreated, err)
	}

	now := time.Now().UTC()
	claimed, ok, err := store.ClaimNext(ctx, "worker-1", now, now.Add(time.Minute))
	if err != nil || !ok || claimed.ID != created.ID {
		t.Fatalf("claim plan = (%#v, %v, %v)", claimed, ok, err)
	}
	if err := store.RenewLease(ctx, created.ID, "worker-1", now.Add(time.Second), now.Add(2*time.Minute)); err != nil {
		t.Fatalf("renew plan lease: %v", err)
	}
	artifact := &iac.ArtifactMetadata{
		MigrationID: migration.ID, JSONPath: "/artifacts/plan.json", TextPath: "/artifacts/plan.txt",
		JSONSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		TextSHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		CreatedAt:  now,
	}
	ready, err := store.Complete(ctx, created.ID, "worker-1", StatusReady, true, artifact, "", now.Add(2*time.Second))
	if err != nil {
		t.Fatalf("complete plan: %v", err)
	}
	if ready.Status != StatusReady || ready.Artifact == nil || ready.HasChanges == nil || !*ready.HasChanges {
		t.Fatalf("ready plan = %#v", ready)
	}
	approved, err := service.Approve(ctx, created.ID, "reviewer@example.com")
	if err != nil {
		t.Fatalf("approve plan: %v", err)
	}
	if approved.Status != StatusApproved || approved.ApprovedBy != "reviewer@example.com" {
		t.Fatalf("approved plan = %#v", approved)
	}
}

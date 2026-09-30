package resources

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/Aritra7/cloudify-platform/internal/plans"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestPostgresManagedResourceProjection(t *testing.T) {
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
	if _, err := database.ExecContext(ctx, "DROP TABLE IF EXISTS managed_resources, terraform_apply_requests, terraform_plan_approvals, terraform_plans, migration_attempts, migration_events, migrations CASCADE"); err != nil {
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

	plan := appliedPlan("11111111-1111-4111-8111-111111111111", "image-one")
	plan.MigrationID = "7b629d1d-7602-4de6-82bd-340fc18e55b6"
	plan.Specification.MigrationID = plan.MigrationID
	plan.UpdatedAt = time.Now().UTC()
	if _, err := database.ExecContext(ctx, `
		INSERT INTO migrations (
			id, idempotency_key, request_hash, status, source_repository_url, source_revision,
			destination_provider, destination_project_id, destination_region, destination_runtime,
			created_at, updated_at
		) VALUES ($1, 'resource-test', $2, 'succeeded', 'https://github.com/example/app', 'main',
			'gcp', 'example-project', 'us-central1', 'cloud-run', $3, $3)`,
		plan.MigrationID, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", plan.UpdatedAt,
	); err != nil {
		t.Fatalf("insert migration: %v", err)
	}
	insertAppliedPlan(t, ctx, database, plan)

	service := NewService(NewPostgresStore(database))
	created, changed, err := service.ProjectApplied(ctx, plan)
	if err != nil || !changed || created.Generation != 1 {
		t.Fatalf("project applied plan = (%#v, %v, %v)", created, changed, err)
	}
	replayed, changed, err := service.ProjectApplied(ctx, plan)
	if err != nil || changed || replayed.Generation != 1 {
		t.Fatalf("replay applied plan = (%#v, %v, %v)", replayed, changed, err)
	}
	listed, err := service.List(ctx, 100)
	if err != nil || len(listed) != 1 || listed[0].ID != created.ID {
		t.Fatalf("list resources = (%#v, %v)", listed, err)
	}
}

func insertAppliedPlan(t *testing.T, ctx context.Context, database *sql.DB, plan plans.Plan) {
	t.Helper()
	specification, err := json.Marshal(plan.Specification)
	if err != nil {
		t.Fatalf("marshal specification: %v", err)
	}
	policy := []byte(`{"allowed":true,"violations":[]}`)
	if _, err := database.ExecContext(ctx, `
		INSERT INTO terraform_plans (
			id, migration_id, idempotency_key, request_hash, status, specification, policy,
			created_at, updated_at
		) VALUES ($1, $2, $3, $4, 'applied', $5, $6, $7, $7)`,
		plan.ID, plan.MigrationID, plan.ID, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		specification, policy, plan.UpdatedAt,
	); err != nil {
		t.Fatalf("insert applied plan: %v", err)
	}
}

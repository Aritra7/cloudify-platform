package iac

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/hashicorp/terraform-exec/tfexec"
	jsonplan "github.com/hashicorp/terraform-json"
)

type fakeTerraformCLI struct {
	initialized bool
	planned     bool
}

func (cli *fakeTerraformCLI) SetStdout(io.Writer) {}
func (cli *fakeTerraformCLI) SetStderr(io.Writer) {}
func (cli *fakeTerraformCLI) Init(context.Context, ...tfexec.InitOption) error {
	cli.initialized = true
	return nil
}
func (cli *fakeTerraformCLI) Plan(context.Context, ...tfexec.PlanOption) (bool, error) {
	cli.planned = true
	return true, nil
}
func (cli *fakeTerraformCLI) ShowPlanFile(context.Context, string, ...tfexec.ShowOption) (*jsonplan.Plan, error) {
	return &jsonplan.Plan{FormatVersion: "1.2"}, nil
}
func (cli *fakeTerraformCLI) ShowPlanFileRaw(context.Context, string, ...tfexec.ShowOption) (string, error) {
	return "Plan: 1 to add, 0 to change, 0 to destroy.\n", nil
}

type recordingArtifactStore struct{ artifact PlanArtifact }

func (store *recordingArtifactStore) Save(_ context.Context, artifact PlanArtifact) (ArtifactMetadata, error) {
	store.artifact = artifact
	return ArtifactMetadata{MigrationID: artifact.MigrationID, CreatedAt: artifact.CreatedAt}, nil
}

func TestPlannerRendersRunsAndStoresPlanEvidence(t *testing.T) {
	t.Parallel()
	cli := &fakeTerraformCLI{}
	artifacts := &recordingArtifactStore{}
	planner := &Planner{
		Artifacts:   artifacts,
		Locker:      &MemoryWorkspaceLocker{},
		BinaryPath:  "/usr/local/bin/terraform",
		StateBucket: "cloudify-terraform-state",
		StatePrefix: "migrations",
		NewCLI: func(string, string) (TerraformCLI, error) {
			return cli, nil
		},
		Now: func() time.Time { return time.Unix(100, 0).UTC() },
	}
	result, err := planner.Plan(context.Background(), t.TempDir(), validDeploymentSpec())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !result.HasChanges || !cli.initialized || !cli.planned {
		t.Fatalf("result = %#v, initialized=%v planned=%v", result, cli.initialized, cli.planned)
	}
	if artifacts.artifact.MigrationID != "migration-123" || len(artifacts.artifact.JSON) == 0 || len(artifacts.artifact.Text) == 0 {
		t.Fatalf("artifact = %#v", artifacts.artifact)
	}
}

func TestBoundedWriterDiscardsBeyondLimitWithoutBreakingCommand(t *testing.T) {
	t.Parallel()
	writer := &boundedWriter{remaining: 4}
	written, err := writer.Write([]byte("123456"))
	if err != nil || written != 6 {
		t.Fatalf("Write = (%d, %v), want (6, nil)", written, err)
	}
	if string(writer.buffer) != "1234" {
		t.Fatalf("buffer = %q, want 1234", writer.buffer)
	}
}

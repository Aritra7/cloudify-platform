package iac

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hashicorp/terraform-exec/tfexec"
	jsonplan "github.com/hashicorp/terraform-json"
)

type fakeTerraformCLI struct {
	initialized   bool
	planned       bool
	applied       bool
	appliedPlan   []byte
	workDirectory string
}

func (cli *fakeTerraformCLI) Apply(context.Context, ...tfexec.ApplyOption) error {
	cli.applied = true
	contents, err := os.ReadFile(filepath.Join(cli.workDirectory, planFilename))
	if err != nil {
		return err
	}
	cli.appliedPlan = contents
	return nil
}

func (cli *fakeTerraformCLI) SetStdout(io.Writer) {}
func (cli *fakeTerraformCLI) SetStderr(io.Writer) {}
func (cli *fakeTerraformCLI) Init(context.Context, ...tfexec.InitOption) error {
	cli.initialized = true
	return nil
}
func (cli *fakeTerraformCLI) Plan(context.Context, ...tfexec.PlanOption) (bool, error) {
	cli.planned = true
	if err := os.WriteFile(filepath.Join(cli.workDirectory, planFilename), []byte("binary-plan"), 0o600); err != nil {
		return false, err
	}
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
		NewCLI: func(workDirectory, _ string) (TerraformCLI, error) {
			cli.workDirectory = workDirectory
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
	if artifacts.artifact.MigrationID != "migration-123" || len(artifacts.artifact.JSON) == 0 || len(artifacts.artifact.Text) == 0 || len(artifacts.artifact.Binary) == 0 {
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

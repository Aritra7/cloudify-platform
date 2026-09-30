package iac

import (
	"context"
	"testing"
)

type binaryStore struct{ plan []byte }

func (store binaryStore) LoadBinary(context.Context, ArtifactMetadata) ([]byte, error) {
	return append([]byte(nil), store.plan...), nil
}

func TestApplierExecutesExactApprovedBinaryPlan(t *testing.T) {
	t.Parallel()
	cli := &fakeTerraformCLI{}
	spec := validDeploymentSpec()
	applier := &Applier{
		Artifacts: binaryStore{plan: []byte("approved-binary-plan")},
		Locker:    &MemoryWorkspaceLocker{}, BinaryPath: "/usr/local/bin/terraform",
		StateBucket: "cloudify-terraform-state", StatePrefix: "migrations",
		NewCLI: func(workDirectory, _ string) (TerraformCLI, error) {
			cli.workDirectory = workDirectory
			return cli, nil
		},
	}
	artifact := ArtifactMetadata{MigrationID: spec.MigrationID}
	if err := applier.Apply(context.Background(), t.TempDir(), spec, artifact); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !cli.applied || string(cli.appliedPlan) != "approved-binary-plan" {
		t.Fatalf("applied = %v, plan = %q", cli.applied, cli.appliedPlan)
	}
}

func TestApplierRejectsArtifactFromAnotherMigration(t *testing.T) {
	t.Parallel()
	spec := validDeploymentSpec()
	applier := &Applier{
		Artifacts: binaryStore{plan: []byte("plan")}, Locker: &MemoryWorkspaceLocker{},
		BinaryPath: "/usr/local/bin/terraform", StateBucket: "cloudify-terraform-state",
	}
	if err := applier.Apply(context.Background(), t.TempDir(), spec, ArtifactMetadata{MigrationID: "different"}); err == nil {
		t.Fatal("Apply returned nil, want migration mismatch error")
	}
}

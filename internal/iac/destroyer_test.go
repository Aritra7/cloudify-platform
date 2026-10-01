package iac

import (
	"context"
	"testing"
)

func TestDestroyerPlansAndAppliesUnderWorkspaceLock(t *testing.T) {
	t.Parallel()
	cli := &fakeTerraformCLI{}
	destroyer := &Destroyer{
		Locker: &MemoryWorkspaceLocker{}, BinaryPath: "/usr/local/bin/terraform",
		StateBucket: "cloudify-terraform-state", StatePrefix: "migrations",
		NewCLI: func(workDirectory, _ string) (TerraformCLI, error) {
			cli.workDirectory = workDirectory
			return cli, nil
		},
	}
	if err := destroyer.Destroy(context.Background(), t.TempDir(), validDeploymentSpec()); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if !cli.initialized || !cli.planned || !cli.applied || string(cli.appliedPlan) != "binary-plan" {
		t.Fatalf("initialized=%v planned=%v applied=%v plan=%q", cli.initialized, cli.planned, cli.applied, cli.appliedPlan)
	}
}

func TestDestroyerValidatesConfiguration(t *testing.T) {
	t.Parallel()
	if err := (&Destroyer{}).Destroy(context.Background(), t.TempDir(), validDeploymentSpec()); err == nil {
		t.Fatal("Destroy returned nil, want configuration error")
	}
}

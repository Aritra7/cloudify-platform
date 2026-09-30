package iac

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hashicorp/terraform-exec/tfexec"
)

// Applier executes only the encrypted binary plan previously bound to approval.
type Applier struct {
	Renderer    Renderer
	Artifacts   BinaryArtifactStore
	Locker      WorkspaceLocker
	BinaryPath  string
	StateBucket string
	StatePrefix string
	NewCLI      CLIFactory
}

func (applier *Applier) Apply(
	ctx context.Context,
	workDirectory string,
	spec DeploymentSpec,
	artifact ArtifactMetadata,
) error {
	if applier.Artifacts == nil || applier.Locker == nil || applier.BinaryPath == "" || applier.StateBucket == "" {
		return errors.New("applier requires artifact storage, workspace locking, a Terraform binary, and a state bucket")
	}
	if !stateBucketPattern.MatchString(applier.StateBucket) || strings.Contains(applier.StateBucket, "..") {
		return errors.New("Terraform state bucket is invalid")
	}
	if filepath.IsAbs(applier.StatePrefix) || strings.Contains(applier.StatePrefix, "..") || strings.Contains(applier.StatePrefix, `\`) {
		return errors.New("Terraform state prefix is invalid")
	}
	if err := spec.Validate(); err != nil {
		return fmt.Errorf("validate deployment specification: %w", err)
	}
	if artifact.MigrationID != spec.MigrationID {
		return errors.New("approved plan belongs to a different migration")
	}
	if applier.NewCLI == nil {
		applier.NewCLI = newTerraformCLI
	}
	release, err := applier.Locker.Acquire(ctx, spec.MigrationID)
	if err != nil {
		return fmt.Errorf("acquire Terraform workspace lock: %w", err)
	}
	defer release()
	if err := applier.Renderer.Render(workDirectory, spec); err != nil {
		return err
	}
	binaryPlan, err := applier.Artifacts.LoadBinary(ctx, artifact)
	if err != nil {
		return fmt.Errorf("load approved binary plan: %w", err)
	}
	planPath := filepath.Join(workDirectory, planFilename)
	if err := os.WriteFile(planPath, binaryPlan, 0o600); err != nil {
		return fmt.Errorf("materialize approved binary plan: %w", err)
	}
	defer func() { _ = os.Remove(planPath) }()
	terraform, err := applier.NewCLI(workDirectory, applier.BinaryPath)
	if err != nil {
		return fmt.Errorf("create Terraform client: %w", err)
	}
	output := &boundedWriter{remaining: maxTerraformOutputBytes}
	terraform.SetStdout(output)
	terraform.SetStderr(output)
	statePrefix := filepath.ToSlash(filepath.Join(applier.StatePrefix, spec.MigrationID))
	if err := terraform.Init(
		ctx,
		tfexec.Upgrade(false),
		tfexec.BackendConfig("bucket="+applier.StateBucket),
		tfexec.BackendConfig("prefix="+statePrefix),
	); err != nil {
		return fmt.Errorf("initialize Terraform for apply: %w", err)
	}
	if err := terraform.Apply(ctx, tfexec.DirOrPlan(planPath), tfexec.LockTimeout("30s")); err != nil {
		return fmt.Errorf("apply approved Terraform plan: %w", err)
	}
	return nil
}

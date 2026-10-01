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

// Destroyer creates a destroy-only plan and applies that exact plan while the
// workspace lock is held. The remote state remains authoritative throughout.
type Destroyer struct {
	Renderer    Renderer
	Locker      WorkspaceLocker
	BinaryPath  string
	StateBucket string
	StatePrefix string
	NewCLI      CLIFactory
}

func (destroyer *Destroyer) Destroy(ctx context.Context, workDirectory string, spec DeploymentSpec) error {
	if destroyer.Locker == nil || destroyer.BinaryPath == "" || destroyer.StateBucket == "" {
		return errors.New("destroyer requires workspace locking, a Terraform binary, and a state bucket")
	}
	if !stateBucketPattern.MatchString(destroyer.StateBucket) || strings.Contains(destroyer.StateBucket, "..") {
		return errors.New("Terraform state bucket is invalid")
	}
	if filepath.IsAbs(destroyer.StatePrefix) || strings.Contains(destroyer.StatePrefix, "..") || strings.Contains(destroyer.StatePrefix, `\`) {
		return errors.New("Terraform state prefix is invalid")
	}
	if err := spec.Validate(); err != nil {
		return fmt.Errorf("validate deployment specification: %w", err)
	}
	if destroyer.NewCLI == nil {
		destroyer.NewCLI = newTerraformCLI
	}
	release, err := destroyer.Locker.Acquire(ctx, spec.MigrationID)
	if err != nil {
		return fmt.Errorf("acquire Terraform workspace lock: %w", err)
	}
	defer release()
	if err := destroyer.Renderer.Render(workDirectory, spec); err != nil {
		return err
	}
	terraform, err := destroyer.NewCLI(workDirectory, destroyer.BinaryPath)
	if err != nil {
		return fmt.Errorf("create Terraform client: %w", err)
	}
	output := &boundedWriter{remaining: maxTerraformOutputBytes}
	terraform.SetStdout(output)
	terraform.SetStderr(output)
	statePrefix := filepath.ToSlash(filepath.Join(destroyer.StatePrefix, spec.MigrationID))
	if err := terraform.Init(
		ctx,
		tfexec.Upgrade(false),
		tfexec.BackendConfig("bucket="+destroyer.StateBucket),
		tfexec.BackendConfig("prefix="+statePrefix),
	); err != nil {
		return fmt.Errorf("initialize Terraform for destroy: %w", err)
	}
	planPath := filepath.Join(workDirectory, planFilename)
	defer func() { _ = os.Remove(planPath) }()
	if _, err := terraform.Plan(ctx, tfexec.Destroy(true), tfexec.Out(planPath), tfexec.LockTimeout("30s")); err != nil {
		return fmt.Errorf("create Terraform destroy plan: %w", err)
	}
	if err := terraform.Apply(ctx, tfexec.DirOrPlan(planPath), tfexec.LockTimeout("30s")); err != nil {
		return fmt.Errorf("apply Terraform destroy plan: %w", err)
	}
	return nil
}

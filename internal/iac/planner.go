package iac

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/hashicorp/terraform-exec/tfexec"
	jsonplan "github.com/hashicorp/terraform-json"
)

const planFilename = "cloudify.tfplan"
const maxTerraformOutputBytes = 1 << 20

var stateBucketPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,61}[a-z0-9]$`)

// TerraformCLI is the tested boundary around terraform-exec.
type TerraformCLI interface {
	SetStdout(io.Writer)
	SetStderr(io.Writer)
	Init(context.Context, ...tfexec.InitOption) error
	Plan(context.Context, ...tfexec.PlanOption) (bool, error)
	ShowPlanFile(context.Context, string, ...tfexec.ShowOption) (*jsonplan.Plan, error)
	ShowPlanFileRaw(context.Context, string, ...tfexec.ShowOption) (string, error)
}

// CLIFactory creates an SDK client for one isolated workspace.
type CLIFactory func(workDirectory, executablePath string) (TerraformCLI, error)

// Planner creates a non-interactive Terraform plan and stores sanitized output.
type Planner struct {
	Renderer    Renderer
	Artifacts   ArtifactStore
	Locker      WorkspaceLocker
	BinaryPath  string
	StateBucket string
	StatePrefix string
	NewCLI      CLIFactory
	Now         func() time.Time
}

// PlanResult links a durable artifact to Terraform's change decision.
type PlanResult struct {
	HasChanges bool             `json:"has_changes"`
	Artifact   ArtifactMetadata `json:"artifact"`
}

func (planner *Planner) Plan(ctx context.Context, workDirectory string, spec DeploymentSpec) (PlanResult, error) {
	if planner.Artifacts == nil || planner.Locker == nil || planner.BinaryPath == "" || planner.StateBucket == "" {
		return PlanResult{}, errors.New("planner requires artifact storage, workspace locking, a Terraform binary, and a state bucket")
	}
	if !stateBucketPattern.MatchString(planner.StateBucket) || strings.Contains(planner.StateBucket, "..") {
		return PlanResult{}, errors.New("Terraform state bucket is invalid")
	}
	if filepath.IsAbs(planner.StatePrefix) || strings.Contains(planner.StatePrefix, "..") || strings.Contains(planner.StatePrefix, `\`) {
		return PlanResult{}, errors.New("Terraform state prefix is invalid")
	}
	if planner.NewCLI == nil {
		planner.NewCLI = newTerraformCLI
	}
	if planner.Now == nil {
		planner.Now = func() time.Time { return time.Now().UTC() }
	}
	if err := spec.Validate(); err != nil {
		return PlanResult{}, fmt.Errorf("validate deployment specification: %w", err)
	}

	release, err := planner.Locker.Acquire(ctx, spec.MigrationID)
	if err != nil {
		return PlanResult{}, fmt.Errorf("acquire Terraform workspace lock: %w", err)
	}
	defer release()
	if err := planner.Renderer.Render(workDirectory, spec); err != nil {
		return PlanResult{}, err
	}
	terraform, err := planner.NewCLI(workDirectory, planner.BinaryPath)
	if err != nil {
		return PlanResult{}, fmt.Errorf("create Terraform client: %w", err)
	}
	output := &boundedWriter{remaining: maxTerraformOutputBytes}
	terraform.SetStdout(output)
	terraform.SetStderr(output)
	statePrefix := filepath.ToSlash(filepath.Join(planner.StatePrefix, spec.MigrationID))
	if err := terraform.Init(
		ctx,
		tfexec.Upgrade(false),
		tfexec.BackendConfig("bucket="+planner.StateBucket),
		tfexec.BackendConfig("prefix="+statePrefix),
	); err != nil {
		return PlanResult{}, fmt.Errorf("initialize Terraform: %w", err)
	}
	planPath := filepath.Join(workDirectory, planFilename)
	defer func() { _ = os.Remove(planPath) }()
	hasChanges, err := terraform.Plan(ctx, tfexec.Out(planPath), tfexec.LockTimeout("30s"))
	if err != nil {
		return PlanResult{}, fmt.Errorf("create Terraform plan: %w", err)
	}
	structured, err := terraform.ShowPlanFile(ctx, planPath)
	if err != nil {
		return PlanResult{}, fmt.Errorf("read structured Terraform plan: %w", err)
	}
	structuredJSON, err := json.MarshalIndent(structured, "", "  ")
	if err != nil {
		return PlanResult{}, fmt.Errorf("encode structured Terraform plan: %w", err)
	}
	humanReadable, err := terraform.ShowPlanFileRaw(ctx, planPath)
	if err != nil {
		return PlanResult{}, fmt.Errorf("read human-readable Terraform plan: %w", err)
	}
	metadata, err := planner.Artifacts.Save(ctx, PlanArtifact{
		MigrationID: spec.MigrationID,
		JSON:        append(structuredJSON, '\n'),
		Text:        []byte(humanReadable),
		CreatedAt:   planner.Now(),
	})
	if err != nil {
		return PlanResult{}, fmt.Errorf("persist Terraform plan artifacts: %w", err)
	}
	return PlanResult{HasChanges: hasChanges, Artifact: metadata}, nil
}

type boundedWriter struct {
	buffer    []byte
	remaining int
}

func (writer *boundedWriter) Write(contents []byte) (int, error) {
	written := len(contents)
	if writer.remaining <= 0 {
		return written, nil
	}
	keep := len(contents)
	if keep > writer.remaining {
		keep = writer.remaining
	}
	writer.buffer = append(writer.buffer, contents[:keep]...)
	writer.remaining -= keep
	return written, nil
}

func newTerraformCLI(workDirectory, executablePath string) (TerraformCLI, error) {
	return tfexec.NewTerraform(workDirectory, executablePath)
}

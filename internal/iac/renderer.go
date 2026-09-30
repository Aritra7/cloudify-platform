package iac

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

//go:embed modules/cloud-run/*.tf
var moduleFiles embed.FS

// Renderer materializes a self-contained Terraform root module.
type Renderer struct{}

// Render validates the specification and writes deterministic configuration.
func (Renderer) Render(workDirectory string, spec DeploymentSpec) error {
	if err := spec.Validate(); err != nil {
		return fmt.Errorf("validate deployment specification: %w", err)
	}
	if err := os.MkdirAll(workDirectory, 0o700); err != nil {
		return fmt.Errorf("create Terraform workspace: %w", err)
	}
	moduleDirectory := filepath.Join(workDirectory, "modules", "cloud-run")
	if err := os.MkdirAll(moduleDirectory, 0o700); err != nil {
		return fmt.Errorf("create Terraform module directory: %w", err)
	}
	entries, err := fs.ReadDir(moduleFiles, "modules/cloud-run")
	if err != nil {
		return fmt.Errorf("read embedded Terraform module: %w", err)
	}
	for _, entry := range entries {
		contents, err := moduleFiles.ReadFile("modules/cloud-run/" + entry.Name())
		if err != nil {
			return fmt.Errorf("read embedded Terraform file %s: %w", entry.Name(), err)
		}
		if err := os.WriteFile(filepath.Join(moduleDirectory, entry.Name()), contents, 0o600); err != nil {
			return fmt.Errorf("write Terraform file %s: %w", entry.Name(), err)
		}
	}

	configuration := rootConfiguration(spec)
	contents, err := json.MarshalIndent(configuration, "", "  ")
	if err != nil {
		return fmt.Errorf("encode Terraform configuration: %w", err)
	}
	contents = append(contents, '\n')
	if err := os.WriteFile(filepath.Join(workDirectory, "main.tf.json"), contents, 0o600); err != nil {
		return fmt.Errorf("write Terraform root configuration: %w", err)
	}
	return nil
}

func rootConfiguration(spec DeploymentSpec) map[string]any {
	return map[string]any{
		"terraform": map[string]any{
			"required_version": ">= 1.6.0, < 2.0.0",
			"required_providers": map[string]any{
				"google": map[string]any{"source": "hashicorp/google", "version": "8.2.0"},
			},
			"backend": map[string]any{"gcs": map[string]any{}},
		},
		"provider": map[string]any{
			"google": map[string]any{"project": spec.ProjectID, "region": spec.Region},
		},
		"module": map[string]any{
			"cloud_run": map[string]any{
				"source":                    "./modules/cloud-run",
				"project_id":                spec.ProjectID,
				"region":                    spec.Region,
				"service_name":              spec.ServiceName,
				"image":                     spec.Image,
				"service_account_email":     spec.ServiceAccountEmail,
				"cpu":                       spec.CPU,
				"memory":                    spec.Memory,
				"min_instances":             spec.MinInstances,
				"max_instances":             spec.MaxInstances,
				"allow_unauthenticated":     spec.AllowUnauthenticated,
				"non_sensitive_environment": spec.NonSensitiveEnvironment,
				"secrets":                   normalizedSecrets(spec.Secrets),
			},
		},
	}
}

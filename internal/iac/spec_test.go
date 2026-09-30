package iac

import (
	"testing"
)

func TestDeploymentSpecValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*DeploymentSpec)
	}{
		{name: "mutable image", mutate: func(spec *DeploymentSpec) { spec.Image = "us-docker.pkg.dev/example-project/apps/api:latest" }},
		{name: "secret in plain environment", mutate: func(spec *DeploymentSpec) { spec.NonSensitiveEnvironment["API_TOKEN"] = "unsafe" }},
		{name: "duplicate secret environment", mutate: func(spec *DeploymentSpec) {
			spec.NonSensitiveEnvironment["DATABASE_URL"] = "not-secret"
			spec.Secrets[0].EnvironmentVariable = "DATABASE_URL"
		}},
		{name: "invalid scaling", mutate: func(spec *DeploymentSpec) { spec.MinInstances = 3; spec.MaxInstances = 2 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			spec := validDeploymentSpec()
			test.mutate(&spec)
			if err := spec.Validate(); err == nil {
				t.Fatal("Validate returned nil, want error")
			}
		})
	}
}

func validDeploymentSpec() DeploymentSpec {
	return DeploymentSpec{
		Version:             SpecificationVersion,
		MigrationID:         "migration-123",
		ProjectID:           "example-project",
		Region:              "us-central1",
		ServiceName:         "example-api",
		Image:               "us-docker.pkg.dev/example-project/apps/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ServiceAccountEmail: "cloud-run@example-project.iam.gserviceaccount.com",
		CPU:                 "1",
		Memory:              "512Mi",
		MinInstances:        0,
		MaxInstances:        3,
		NonSensitiveEnvironment: map[string]string{
			"SPRING_PROFILES_ACTIVE": "production",
		},
		Secrets: []SecretReference{{EnvironmentVariable: "DATABASE_URL", Secret: "database-url", Version: "latest"}},
	}
}

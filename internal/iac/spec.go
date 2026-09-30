package iac

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

const SpecificationVersion = "cloudify.dev/v1alpha1"

var (
	gcpProjectPattern  = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`)
	regionPattern      = regexp.MustCompile(`^[a-z]+-[a-z]+[0-9]$`)
	servicePattern     = regexp.MustCompile(`^[a-z][a-z0-9-]{0,47}[a-z0-9]$`)
	environmentPattern = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
	secretPattern      = regexp.MustCompile(`^[A-Za-z0-9_-]{1,255}$`)
	digestPattern      = regexp.MustCompile(`@sha256:[a-f0-9]{64}$`)
	cpuPattern         = regexp.MustCompile(`^(1|2|4|6|8|[1-9][0-9]{2,3}m)$`)
	memoryPattern      = regexp.MustCompile(`^[1-9][0-9]*(Mi|Gi)$`)
)

// DeploymentSpec is the validated boundary between migration analysis and IaC.
// It deliberately contains secret references but never secret values.
type DeploymentSpec struct {
	Version                 string            `json:"version"`
	MigrationID             string            `json:"migration_id"`
	ProjectID               string            `json:"project_id"`
	Region                  string            `json:"region"`
	ServiceName             string            `json:"service_name"`
	Image                   string            `json:"image"`
	ServiceAccountEmail     string            `json:"service_account_email"`
	CPU                     string            `json:"cpu"`
	Memory                  string            `json:"memory"`
	MinInstances            int               `json:"min_instances"`
	MaxInstances            int               `json:"max_instances"`
	AllowUnauthenticated    bool              `json:"allow_unauthenticated"`
	NonSensitiveEnvironment map[string]string `json:"non_sensitive_environment,omitempty"`
	Secrets                 []SecretReference `json:"secrets,omitempty"`
}

// SecretReference maps an environment variable to a Secret Manager version.
type SecretReference struct {
	EnvironmentVariable string `json:"environment_variable"`
	Secret              string `json:"secret"`
	Version             string `json:"version"`
}

// Validate rejects ambiguous, mutable, or potentially secret-bearing input.
func (spec DeploymentSpec) Validate() error {
	if spec.Version != SpecificationVersion {
		return fmt.Errorf("version must be %q", SpecificationVersion)
	}
	if !artifactIDPattern.MatchString(spec.MigrationID) {
		return errors.New("migration_id contains unsupported characters")
	}
	if !gcpProjectPattern.MatchString(spec.ProjectID) {
		return errors.New("project_id is not a valid GCP project ID")
	}
	if !regionPattern.MatchString(spec.Region) {
		return errors.New("region is not a valid GCP region")
	}
	if !servicePattern.MatchString(spec.ServiceName) {
		return errors.New("service_name must be a valid Cloud Run service name")
	}
	if !digestPattern.MatchString(spec.Image) {
		return errors.New("image must use an immutable sha256 digest")
	}
	if !strings.HasSuffix(spec.ServiceAccountEmail, ".iam.gserviceaccount.com") {
		return errors.New("service_account_email must be a GCP service account")
	}
	if !cpuPattern.MatchString(spec.CPU) || !memoryPattern.MatchString(spec.Memory) {
		return errors.New("cpu or memory uses an unsupported Cloud Run quantity")
	}
	if spec.MinInstances < 0 || spec.MaxInstances < 1 || spec.MinInstances > spec.MaxInstances {
		return errors.New("instance bounds must satisfy 0 <= min_instances <= max_instances")
	}

	usedEnvironment := make(map[string]struct{}, len(spec.NonSensitiveEnvironment)+len(spec.Secrets))
	for name := range spec.NonSensitiveEnvironment {
		if !environmentPattern.MatchString(name) {
			return fmt.Errorf("environment variable %q is invalid", name)
		}
		upperName := strings.ToUpper(name)
		for _, marker := range []string{"SECRET", "TOKEN", "PASSWORD", "API_KEY", "PRIVATE_KEY"} {
			if strings.Contains(upperName, marker) {
				return fmt.Errorf("environment variable %q may contain a secret; use a Secret Manager reference", name)
			}
		}
		usedEnvironment[name] = struct{}{}
	}
	for index, secret := range spec.Secrets {
		if !environmentPattern.MatchString(secret.EnvironmentVariable) {
			return fmt.Errorf("secrets[%d].environment_variable is invalid", index)
		}
		if !secretPattern.MatchString(secret.Secret) {
			return fmt.Errorf("secrets[%d].secret is invalid", index)
		}
		if !secretPattern.MatchString(secret.Version) {
			return fmt.Errorf("secrets[%d].version is invalid", index)
		}
		if _, duplicate := usedEnvironment[secret.EnvironmentVariable]; duplicate {
			return fmt.Errorf("environment variable %q is configured more than once", secret.EnvironmentVariable)
		}
		usedEnvironment[secret.EnvironmentVariable] = struct{}{}
	}
	return nil
}

func normalizedSecrets(secrets []SecretReference) []SecretReference {
	result := append([]SecretReference(nil), secrets...)
	sort.Slice(result, func(i, j int) bool {
		return result[i].EnvironmentVariable < result[j].EnvironmentVariable
	})
	return result
}

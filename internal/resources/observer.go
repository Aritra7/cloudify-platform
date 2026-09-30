package resources

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	iampb "cloud.google.com/go/iam/apiv1/iampb"
	run "cloud.google.com/go/run/apiv2"
	"cloud.google.com/go/run/apiv2/runpb"
	"github.com/Aritra7/cloudify-platform/internal/iac"
	"github.com/googleapis/gax-go/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Observer interface {
	Observe(context.Context, Resource) (Observation, error)
}

// Observation contains only fields owned by Cloudify's Terraform module.
// Secret Manager values are never read or persisted.
type Observation struct {
	Exists                     bool                           `json:"exists"`
	ProviderGeneration         int64                          `json:"provider_generation,omitempty"`
	ProviderObservedGeneration int64                          `json:"provider_observed_generation,omitempty"`
	Reconciling                bool                           `json:"reconciling,omitempty"`
	Image                      string                         `json:"image,omitempty"`
	ServiceAccountEmail        string                         `json:"service_account_email,omitempty"`
	CPU                        string                         `json:"cpu,omitempty"`
	Memory                     string                         `json:"memory,omitempty"`
	MinInstances               int                            `json:"min_instances,omitempty"`
	MaxInstances               int                            `json:"max_instances,omitempty"`
	AllowUnauthenticated       bool                           `json:"allow_unauthenticated"`
	NonSensitiveEnvironment    map[string]string              `json:"non_sensitive_environment,omitempty"`
	Secrets                    map[string]iac.SecretReference `json:"secrets,omitempty"`
	ContainerCount             int                            `json:"container_count,omitempty"`
}

type cloudRunAPI interface {
	GetService(context.Context, *runpb.GetServiceRequest, ...gax.CallOption) (*runpb.Service, error)
	GetIamPolicy(context.Context, *iampb.GetIamPolicyRequest, ...gax.CallOption) (*iampb.Policy, error)
}

type CloudRunObserver struct{ Client cloudRunAPI }

func NewCloudRunObserver(client *run.ServicesClient) *CloudRunObserver {
	return &CloudRunObserver{Client: client}
}

func (observer *CloudRunObserver) Observe(ctx context.Context, resource Resource) (Observation, error) {
	if observer.Client == nil {
		return Observation{}, fmt.Errorf("Cloud Run observer requires a client")
	}
	name := fmt.Sprintf("projects/%s/locations/%s/services/%s", resource.ProjectID, resource.Region, resource.Name)
	service, err := observer.Client.GetService(ctx, &runpb.GetServiceRequest{Name: name})
	if status.Code(err) == codes.NotFound {
		return Observation{Exists: false}, nil
	}
	if err != nil {
		return Observation{}, fmt.Errorf("get Cloud Run service: %w", err)
	}
	policy, err := observer.Client.GetIamPolicy(ctx, &iampb.GetIamPolicyRequest{Resource: name})
	if err != nil {
		return Observation{}, fmt.Errorf("get Cloud Run IAM policy: %w", err)
	}
	result := observationFromService(service, resource.ProjectID)
	for _, binding := range policy.GetBindings() {
		if binding.GetRole() != "roles/run.invoker" {
			continue
		}
		for _, member := range binding.GetMembers() {
			if member == "allUsers" {
				result.AllowUnauthenticated = true
			}
		}
	}
	return result, nil
}

func observationFromService(service *runpb.Service, projectID string) Observation {
	result := Observation{
		Exists: true, ProviderGeneration: service.GetGeneration(),
		ProviderObservedGeneration: service.GetObservedGeneration(), Reconciling: service.GetReconciling(),
		AllowUnauthenticated:    service.GetInvokerIamDisabled(),
		NonSensitiveEnvironment: make(map[string]string), Secrets: make(map[string]iac.SecretReference),
	}
	template := service.GetTemplate()
	result.ServiceAccountEmail = template.GetServiceAccount()
	result.MinInstances = int(template.GetScaling().GetMinInstanceCount())
	result.MaxInstances = int(template.GetScaling().GetMaxInstanceCount())
	containers := template.GetContainers()
	result.ContainerCount = len(containers)
	if len(containers) == 0 {
		return result
	}
	container := containers[0]
	result.Image = container.GetImage()
	result.CPU = container.GetResources().GetLimits()["cpu"]
	result.Memory = container.GetResources().GetLimits()["memory"]
	for _, environment := range container.GetEnv() {
		if source := environment.GetValueSource(); source != nil && source.GetSecretKeyRef() != nil {
			secret := source.GetSecretKeyRef()
			result.Secrets[environment.GetName()] = iac.SecretReference{
				EnvironmentVariable: environment.GetName(), Secret: normalizedSecretName(secret.GetSecret(), projectID), Version: secret.GetVersion(),
			}
			continue
		}
		result.NonSensitiveEnvironment[environment.GetName()] = environment.GetValue()
	}
	return result
}

func classify(desired iac.DeploymentSpec, observed Observation) State {
	if !observed.Exists {
		return StateMissing
	}
	desiredSecrets := make(map[string]iac.SecretReference, len(desired.Secrets))
	for _, secret := range desired.Secrets {
		desiredSecrets[secret.EnvironmentVariable] = secret
	}
	if observed.ContainerCount != 1 || observed.Image != desired.Image ||
		observed.ServiceAccountEmail != desired.ServiceAccountEmail || canonicalCPU(observed.CPU) != canonicalCPU(desired.CPU) ||
		observed.Memory != desired.Memory || observed.MinInstances != desired.MinInstances ||
		observed.MaxInstances != desired.MaxInstances || observed.AllowUnauthenticated != desired.AllowUnauthenticated ||
		!reflect.DeepEqual(observed.NonSensitiveEnvironment, normalizedEnvironment(desired.NonSensitiveEnvironment)) ||
		!reflect.DeepEqual(observed.Secrets, desiredSecrets) {
		return StateDrifted
	}
	return StateInSync
}

func canonicalCPU(value string) string {
	if strings.HasSuffix(value, "m") {
		return value
	}
	cores, err := strconv.Atoi(value)
	if err != nil {
		return value
	}
	return strconv.Itoa(cores*1000) + "m"
}

func normalizedSecretName(value, projectID string) string {
	return strings.TrimPrefix(value, "projects/"+projectID+"/secrets/")
}

func normalizedEnvironment(environment map[string]string) map[string]string {
	if len(environment) == 0 {
		return map[string]string{}
	}
	result := make(map[string]string, len(environment))
	keys := make([]string, 0, len(environment))
	for key := range environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		result[key] = environment[key]
	}
	return result
}

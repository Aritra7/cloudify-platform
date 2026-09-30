package resources

import (
	"testing"

	"cloud.google.com/go/run/apiv2/runpb"
)

func TestObservationFromCloudRunServiceUsesOnlyManagedFields(t *testing.T) {
	t.Parallel()
	service := &runpb.Service{
		Generation: 4, ObservedGeneration: 4,
		Template: &runpb.RevisionTemplate{
			ServiceAccount: "cloud-run@example-project.iam.gserviceaccount.com",
			Scaling:        &runpb.RevisionScaling{MinInstanceCount: 1, MaxInstanceCount: 3},
			Containers: []*runpb.Container{{
				Image:     "us-docker.pkg.dev/example-project/apps/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				Resources: &runpb.ResourceRequirements{Limits: map[string]string{"cpu": "1", "memory": "512Mi"}},
				Env: []*runpb.EnvVar{
					{Name: "MODE", Values: &runpb.EnvVar_Value{Value: "production"}},
					{Name: "DATABASE_PASSWORD", Values: &runpb.EnvVar_ValueSource{ValueSource: &runpb.EnvVarSource{
						SecretKeyRef: &runpb.SecretKeySelector{Secret: "database-password", Version: "7"},
					}}},
				},
			}},
		},
	}
	observed := observationFromService(service, "example-project")
	if observed.ProviderGeneration != 4 || observed.ContainerCount != 1 || observed.CPU != "1" ||
		observed.NonSensitiveEnvironment["MODE"] != "production" || observed.Secrets["DATABASE_PASSWORD"].Version != "7" {
		t.Fatalf("observation = %#v", observed)
	}
}

func TestClassifyDetectsConfigurationAndDeletionDrift(t *testing.T) {
	t.Parallel()
	_, resource := projectedResource(t)
	observed := matchingObservation(resource)
	if state := classify(resource.Desired, observed); state != StateInSync {
		t.Fatalf("matching state = %q", state)
	}
	observed.Image = "us-docker.pkg.dev/example-project/apps/api@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if state := classify(resource.Desired, observed); state != StateDrifted {
		t.Fatalf("changed image state = %q", state)
	}
	if state := classify(resource.Desired, Observation{Exists: false}); state != StateMissing {
		t.Fatalf("missing state = %q", state)
	}
}

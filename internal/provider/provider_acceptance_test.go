package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestAccMigrationLifecycle(t *testing.T) {
	server := newAcceptanceAPI(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: acceptanceProviderFactories(),
		Steps: []resource.TestStep{
			{
				Config: acceptanceProviderConfig(server.URL) + `
resource "cloudify_migration" "test" {
  idempotency_key = "acceptance-main"
  repository_url  = "https://github.com/example/application"
  revision        = "main"
  project_id      = "example-project"
  region          = "us-central1"
  runtime         = "cloud-run"
}`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("cloudify_migration.test", "id", "migration-acceptance"),
					resource.TestCheckResourceAttr("cloudify_migration.test", "status", "succeeded"),
					resource.TestCheckResourceAttr("cloudify_migration.test", "attempt_count", "1"),
				),
			},
			{
				ResourceName:      "cloudify_migration.test",
				ImportState:       true,
				ImportStateId:     "migration-acceptance",
				ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{
					"idempotency_key", "timeouts",
				},
			},
		},
	})
}

func TestAccManagedResourceLifecycle(t *testing.T) {
	server := newAcceptanceAPI(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: acceptanceProviderFactories(),
		CheckDestroy: func(*terraform.State) error {
			if lifecycle := server.resourceLifecycle(); lifecycle != "deleted" {
				return fmt.Errorf("managed resource lifecycle = %q, want deleted", lifecycle)
			}
			return nil
		},
		Steps: []resource.TestStep{{
			Config: acceptanceProviderConfig(server.URL) + `
resource "cloudify_resource" "test" {
  id = "resource-acceptance"
}`,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("cloudify_resource.test", "name", "acceptance-api"),
				resource.TestCheckResourceAttr("cloudify_resource.test", "state", "in_sync"),
				resource.TestCheckResourceAttr("cloudify_resource.test", "lifecycle_status", "active"),
			),
		}},
	})
}

func acceptanceProviderFactories() map[string]func() (tfprotov6.ProviderServer, error) {
	return map[string]func() (tfprotov6.ProviderServer, error){
		"cloudify": providerserver.NewProtocol6WithError(New("acceptance")()),
	}
}

func acceptanceProviderConfig(endpoint string) string {
	return fmt.Sprintf(`
provider "cloudify" { endpoint = %q }
`, endpoint)
}

type acceptanceAPI struct {
	*httptest.Server
	t             *testing.T
	mu            sync.Mutex
	lifecycle     string
	deletionActor string
}

func newAcceptanceAPI(t *testing.T) *acceptanceAPI {
	t.Helper()
	api := &acceptanceAPI{t: t, lifecycle: "active"}
	api.Server = httptest.NewServer(http.HandlerFunc(api.serveHTTP))
	t.Cleanup(api.Close)
	return api
}

func (api *acceptanceAPI) resourceLifecycle() string {
	api.mu.Lock()
	defer api.mu.Unlock()
	return api.lifecycle
}

func (api *acceptanceAPI) serveHTTP(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Content-Type", "application/json")
	switch {
	case request.Method == http.MethodPost && request.URL.Path == "/v1/migrations":
		response.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(response).Encode(acceptanceMigration())
	case request.Method == http.MethodGet && request.URL.Path == "/v1/migrations/migration-acceptance":
		_ = json.NewEncoder(response).Encode(acceptanceMigration())
	case request.URL.Path == "/v1/resources/resource-acceptance":
		api.mu.Lock()
		if request.Method == http.MethodDelete {
			api.lifecycle = "deleted"
			response.WriteHeader(http.StatusAccepted)
		}
		managed := acceptanceManagedResource(api.lifecycle)
		api.mu.Unlock()
		_ = json.NewEncoder(response).Encode(managed)
	default:
		api.t.Errorf("unexpected acceptance API request: %s %s", request.Method, request.URL.Path)
		response.WriteHeader(http.StatusNotFound)
		_, _ = response.Write([]byte(`{"error":{"code":"not_found","message":"not found"}}`))
	}
}

func acceptanceMigration() Migration {
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	return Migration{
		ID: "migration-acceptance", Status: "succeeded", AttemptCount: 1,
		Source:      Source{RepositoryURL: "https://github.com/example/application", Revision: "main"},
		Destination: Destination{Provider: "gcp", ProjectID: "example-project", Region: "us-central1", Runtime: "cloud-run"},
		CreatedAt:   now, UpdatedAt: now.Add(time.Minute),
	}
}

func acceptanceManagedResource(lifecycle string) ManagedResource {
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	resource := ManagedResource{
		ID: "resource-acceptance", Kind: "cloud_run_service", MigrationID: "migration-acceptance",
		SourcePlanID: "plan-acceptance", ProjectID: "example-project", Region: "us-central1", Name: "acceptance-api",
		Desired: json.RawMessage(`{"service_name":"acceptance-api"}`), Observed: json.RawMessage(`{"exists":true}`),
		State: "in_sync", RemediationPolicy: "automatic", Lifecycle: lifecycle,
		Generation: 1, ObservedGeneration: 1, CreatedAt: now, UpdatedAt: now.Add(time.Minute),
	}
	if lifecycle == "deleted" {
		deletedAt := now.Add(2 * time.Minute)
		resource.State = "missing"
		resource.DeletedAt = &deletedAt
	}
	return resource
}

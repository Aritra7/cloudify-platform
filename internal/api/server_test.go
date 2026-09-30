package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Aritra7/cloudify-platform/internal/auth"
	"github.com/Aritra7/cloudify-platform/internal/events"
	"github.com/Aritra7/cloudify-platform/internal/iac"
	"github.com/Aritra7/cloudify-platform/internal/migrations"
	"github.com/Aritra7/cloudify-platform/internal/observability"
	"github.com/Aritra7/cloudify-platform/internal/plans"
	"github.com/Aritra7/cloudify-platform/internal/resources"
)

func TestOperationalEndpoints(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		path       string
		wantStatus string
	}{
		{name: "health", path: "/healthz", wantStatus: "ok"},
		{name: "readiness", path: "/readyz", wantStatus: "ready"},
	}

	server := newTestServer()
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, request)

			if response.Code != http.StatusOK {
				t.Fatalf("status code = %d, want %d", response.Code, http.StatusOK)
			}

			var body statusResponse
			if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body.Status != test.wantStatus {
				t.Fatalf("status = %q, want %q", body.Status, test.wantStatus)
			}
		})
	}
}

func TestUnknownRoute(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest(http.MethodGet, "/missing", nil)
	response := httptest.NewRecorder()
	newTestServer().Handler().ServeHTTP(response, request)

	if response.Code != http.StatusNotFound {
		t.Fatalf("status code = %d, want %d", response.Code, http.StatusNotFound)
	}
}

func TestMetricsEndpoint(t *testing.T) {
	t.Parallel()
	request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	response := httptest.NewRecorder()
	newTestServer().Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if !strings.Contains(response.Body.String(), "cloudify_dispatcher_claims_total 0") {
		t.Fatalf("metrics response missing dispatcher claims: %s", response.Body.String())
	}
}

func TestRetryAndAttemptHistoryEndpoints(t *testing.T) {
	t.Parallel()
	store := migrations.NewMemoryStore()
	service := migrations.NewService(store)
	server := NewServer(service, events.NewMemoryStore())
	migration, _, err := service.Create(context.Background(), "retry-request", migrations.CreateRequest{
		Source:      migrations.Source{RepositoryURL: "https://github.com/example/application", Revision: "main"},
		Destination: migrations.Destination{Provider: "gcp", ProjectID: "example-project", Region: "us-central1", Runtime: "cloud-run"},
	})
	if err != nil {
		t.Fatalf("create migration: %v", err)
	}
	now := time.Unix(100, 0).UTC()
	if _, claimed, err := store.ClaimNext(context.Background(), "worker-1", now, now.Add(time.Minute)); err != nil || !claimed {
		t.Fatalf("claim = (%v, %v)", claimed, err)
	}
	if _, err := store.Complete(context.Background(), migration.ID, "worker-1", migrations.StatusFailed, now.Add(time.Second)); err != nil {
		t.Fatalf("complete: %v", err)
	}

	retryRequest := httptest.NewRequest(http.MethodPost, "/v1/migrations/"+migration.ID+"/retry", nil)
	retryResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(retryResponse, retryRequest)
	if retryResponse.Code != http.StatusAccepted {
		t.Fatalf("retry status = %d, want %d: %s", retryResponse.Code, http.StatusAccepted, retryResponse.Body.String())
	}

	attemptRequest := httptest.NewRequest(http.MethodGet, "/v1/migrations/"+migration.ID+"/attempts", nil)
	attemptResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(attemptResponse, attemptRequest)
	if attemptResponse.Code != http.StatusOK || !strings.Contains(attemptResponse.Body.String(), `"status":"failed"`) {
		t.Fatalf("attempt response = %d: %s", attemptResponse.Code, attemptResponse.Body.String())
	}
}

func TestCreateGetAndCancelMigration(t *testing.T) {
	t.Parallel()

	server := newTestServer()
	body := `{
      "source": {
        "repository_url": "https://github.com/example/application",
        "revision": "main"
      },
      "destination": {
        "provider": "gcp",
        "project_id": "example-project",
        "region": "us-central1",
        "runtime": "cloud-run"
      }
    }`

	createRequest := httptest.NewRequest(http.MethodPost, "/v1/migrations", strings.NewReader(body))
	createRequest.Header.Set("Idempotency-Key", "request-1")
	createResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(createResponse, createRequest)
	if createResponse.Code != http.StatusAccepted {
		t.Fatalf("create status = %d, want %d: %s", createResponse.Code, http.StatusAccepted, createResponse.Body.String())
	}

	var created migrations.Migration
	if err := json.NewDecoder(createResponse.Body).Decode(&created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.Status != migrations.StatusQueued {
		t.Fatalf("created status = %q, want %q", created.Status, migrations.StatusQueued)
	}
	if got := createResponse.Header().Get("Location"); got != "/v1/migrations/"+created.ID {
		t.Fatalf("Location = %q", got)
	}

	getRequest := httptest.NewRequest(http.MethodGet, "/v1/migrations/"+created.ID, nil)
	getResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(getResponse, getRequest)
	if getResponse.Code != http.StatusOK {
		t.Fatalf("get status = %d, want %d", getResponse.Code, http.StatusOK)
	}

	cancelRequest := httptest.NewRequest(http.MethodPost, "/v1/migrations/"+created.ID+"/cancel", nil)
	cancelResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(cancelResponse, cancelRequest)
	if cancelResponse.Code != http.StatusOK {
		t.Fatalf("cancel status = %d, want %d: %s", cancelResponse.Code, http.StatusOK, cancelResponse.Body.String())
	}
	var cancelled migrations.Migration
	if err := json.NewDecoder(cancelResponse.Body).Decode(&cancelled); err != nil {
		t.Fatalf("decode cancel response: %v", err)
	}
	if cancelled.Status != migrations.StatusCancelled {
		t.Fatalf("cancelled status = %q, want %q", cancelled.Status, migrations.StatusCancelled)
	}
}

func TestCreateMigrationIdempotency(t *testing.T) {
	t.Parallel()

	server := newTestServer()
	body := `{
      "source":{"repository_url":"https://github.com/example/application","revision":"main"},
      "destination":{"provider":"gcp","project_id":"example-project","region":"us-central1","runtime":"cloud-run"}
    }`

	first := httptest.NewRequest(http.MethodPost, "/v1/migrations", strings.NewReader(body))
	first.Header.Set("Idempotency-Key", "request-1")
	firstResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(firstResponse, first)

	replay := httptest.NewRequest(http.MethodPost, "/v1/migrations", strings.NewReader(body))
	replay.Header.Set("Idempotency-Key", "request-1")
	replayResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(replayResponse, replay)

	if replayResponse.Code != http.StatusOK {
		t.Fatalf("replay status = %d, want %d", replayResponse.Code, http.StatusOK)
	}
	if replayResponse.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatal("replay response did not identify the idempotent replay")
	}
}

func TestCreateMigrationValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		key        string
		body       string
		wantStatus int
	}{
		{
			name:       "missing idempotency key",
			body:       `{}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "unknown field",
			key:        "request-1",
			body:       `{"unknown":true}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "unsupported provider",
			key:        "request-2",
			body:       `{"source":{"repository_url":"https://github.com/example/application","revision":"main"},"destination":{"provider":"aws","project_id":"example-project","region":"us-east-1","runtime":"cloud-run"}}`,
			wantStatus: http.StatusUnprocessableEntity,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			request := httptest.NewRequest(http.MethodPost, "/v1/migrations", strings.NewReader(test.body))
			request.Header.Set("Idempotency-Key", test.key)
			response := httptest.NewRecorder()
			newTestServer().Handler().ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d: %s", response.Code, test.wantStatus, response.Body.String())
			}
		})
	}
}

func TestMigrationEventStreamIsOrderedAndResumable(t *testing.T) {
	t.Parallel()

	server := newTestServer()
	migration, _, err := server.migrations.Create(context.Background(), "event-request", migrations.CreateRequest{
		Source: migrations.Source{RepositoryURL: "https://github.com/example/application", Revision: "main"},
		Destination: migrations.Destination{
			Provider: "gcp", ProjectID: "example-project", Region: "us-central1", Runtime: "cloud-run",
		},
	})
	if err != nil {
		t.Fatalf("create migration: %v", err)
	}
	first, err := server.events.Append(context.Background(), events.Event{
		MigrationID: migration.ID,
		Kind:        "worker_output",
		Phase:       "checkout",
		Stream:      "stdout",
		Message:     "first",
		CreatedAt:   time.Unix(100, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("append first event: %v", err)
	}
	second, err := server.events.Append(context.Background(), events.Event{
		MigrationID: migration.ID,
		Kind:        "worker_output",
		Phase:       "migration",
		Stream:      "stderr",
		Message:     "second",
		CreatedAt:   time.Unix(101, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("append second event: %v", err)
	}
	if _, err := server.migrations.Cancel(context.Background(), migration.ID); err != nil {
		t.Fatalf("cancel migration: %v", err)
	}

	request := httptest.NewRequest(
		http.MethodGet,
		"/v1/migrations/"+migration.ID+"/events/stream?after="+strconv.FormatInt(first.Sequence, 10),
		nil,
	)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("stream status = %d, want %d", response.Code, http.StatusOK)
	}
	body := response.Body.String()
	if strings.Contains(body, "first") {
		t.Fatalf("stream replayed an event before the cursor: %s", body)
	}
	if !strings.Contains(body, "id: "+strconv.FormatInt(second.Sequence, 10)) || !strings.Contains(body, `"message":"second"`) {
		t.Fatalf("stream did not contain second event: %s", body)
	}
}

func TestTerraformPlanCreateReadAndApprove(t *testing.T) {
	t.Parallel()
	migrationStore := migrations.NewMemoryStore()
	migrationService := migrations.NewService(migrationStore)
	migration, _, err := migrationService.Create(context.Background(), "migration-request", migrations.CreateRequest{
		Source:      migrations.Source{RepositoryURL: "https://github.com/example/application", Revision: "main"},
		Destination: migrations.Destination{Provider: "gcp", ProjectID: "example-project", Region: "us-central1", Runtime: "cloud-run"},
	})
	if err != nil {
		t.Fatalf("create migration: %v", err)
	}
	now := time.Unix(100, 0).UTC()
	if _, claimed, err := migrationStore.ClaimNext(context.Background(), "migration-worker", now, now.Add(time.Minute)); err != nil || !claimed {
		t.Fatalf("claim migration = (%v, %v)", claimed, err)
	}
	if _, err := migrationStore.Complete(context.Background(), migration.ID, "migration-worker", migrations.StatusSucceeded, now.Add(time.Second)); err != nil {
		t.Fatalf("complete migration: %v", err)
	}
	planStore := plans.NewMemoryStore()
	planService := plans.NewService(planStore, plans.DefaultPolicy())
	token := "test-operator-token"
	tokenDigest := sha256.Sum256([]byte(token))
	authenticator, err := auth.NewBearerAuthenticator(`[{"actor":"operator@example.com","token_sha256":"` + fmt.Sprintf("%x", tokenDigest) + `","roles":["planner","approver","operator"]}]`)
	if err != nil {
		t.Fatalf("create authenticator: %v", err)
	}
	server := NewAuthenticatedServer(migrationService, events.NewMemoryStore(), planService, &observability.Metrics{}, authenticator)
	specification := iac.DeploymentSpec{
		Version: iac.SpecificationVersion, ProjectID: "example-project", Region: "us-central1", ServiceName: "example-api",
		Image:               "us-docker.pkg.dev/example-project/apps/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ServiceAccountEmail: "cloud-run@example-project.iam.gserviceaccount.com",
		CPU:                 "1", Memory: "512Mi", MaxInstances: 3,
	}
	body, err := json.Marshal(specification)
	if err != nil {
		t.Fatalf("encode specification: %v", err)
	}
	createRequest := httptest.NewRequest(http.MethodPost, "/v1/migrations/"+migration.ID+"/plans", strings.NewReader(string(body)))
	createRequest.Header.Set("Idempotency-Key", "plan-request")
	createRequest.Header.Set("Authorization", "Bearer "+token)
	createResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(createResponse, createRequest)
	if createResponse.Code != http.StatusAccepted {
		t.Fatalf("create plan status = %d: %s", createResponse.Code, createResponse.Body.String())
	}
	var created plans.Plan
	if err := json.NewDecoder(createResponse.Body).Decode(&created); err != nil {
		t.Fatalf("decode plan: %v", err)
	}
	if _, claimed, err := planStore.ClaimNext(context.Background(), "terraform-worker", now, now.Add(time.Minute)); err != nil || !claimed {
		t.Fatalf("claim plan = (%v, %v)", claimed, err)
	}
	artifact := &iac.ArtifactMetadata{
		MigrationID: migration.ID, JSONPath: "/plan.json", TextPath: "/plan.txt", CreatedAt: now,
		JSONSHA256:      "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		TextSHA256:      "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		BinaryObjectKey: migration.ID + "/plan.enc",
		BinarySHA256:    "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
	}
	if _, err := planStore.Complete(context.Background(), created.ID, "terraform-worker", plans.StatusReady, true, artifact, "", now); err != nil {
		t.Fatalf("complete plan: %v", err)
	}
	approveRequest := httptest.NewRequest(http.MethodPost, "/v1/plans/"+created.ID+"/approve", nil)
	approveRequest.Header.Set("Authorization", "Bearer "+token)
	approveResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(approveResponse, approveRequest)
	if approveResponse.Code != http.StatusOK {
		t.Fatalf("approve status = %d: %s", approveResponse.Code, approveResponse.Body.String())
	}
	var approved plans.Plan
	if err := json.NewDecoder(approveResponse.Body).Decode(&approved); err != nil {
		t.Fatalf("decode approved plan: %v", err)
	}
	if approved.Status != plans.StatusApproved || approved.ApprovedBy != "operator@example.com" {
		t.Fatalf("approved plan = %#v", approved)
	}
	applyRequest := httptest.NewRequest(http.MethodPost, "/v1/plans/"+created.ID+"/apply", nil)
	applyRequest.Header.Set("Authorization", "Bearer "+token)
	applyResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(applyResponse, applyRequest)
	if applyResponse.Code != http.StatusAccepted {
		t.Fatalf("apply status = %d: %s", applyResponse.Code, applyResponse.Body.String())
	}
}

func TestManagedResourceListAndGet(t *testing.T) {
	t.Parallel()
	resourceService := resources.NewService(resources.NewMemoryStore())
	plan := plans.Plan{
		ID: "plan-1", MigrationID: "7b629d1d-7602-4de6-82bd-340fc18e55b6", Status: plans.StatusApplied,
		Specification: iac.DeploymentSpec{
			Version: iac.SpecificationVersion, MigrationID: "7b629d1d-7602-4de6-82bd-340fc18e55b6",
			ProjectID: "example-project", Region: "us-central1", ServiceName: "example-api",
			Image:               "us-docker.pkg.dev/example-project/apps/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			ServiceAccountEmail: "cloud-run@example-project.iam.gserviceaccount.com",
			CPU:                 "1", Memory: "512Mi", MaxInstances: 3,
		},
	}
	projected, _, err := resourceService.ProjectApplied(context.Background(), plan)
	if err != nil {
		t.Fatalf("project applied plan: %v", err)
	}
	server := NewServerWithResources(
		migrations.NewService(migrations.NewMemoryStore()), events.NewMemoryStore(), nil,
		resourceService, &observability.Metrics{},
	)
	listResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(listResponse, httptest.NewRequest(http.MethodGet, "/v1/resources?limit=10", nil))
	if listResponse.Code != http.StatusOK || !strings.Contains(listResponse.Body.String(), `"name":"example-api"`) {
		t.Fatalf("list response = %d: %s", listResponse.Code, listResponse.Body.String())
	}
	getResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(getResponse, httptest.NewRequest(http.MethodGet, "/v1/resources/"+projected.ID, nil))
	if getResponse.Code != http.StatusOK || !strings.Contains(getResponse.Body.String(), `"generation":1`) {
		t.Fatalf("get response = %d: %s", getResponse.Code, getResponse.Body.String())
	}
}

func newTestServer() *Server {
	return NewServer(migrations.NewService(migrations.NewMemoryStore()), events.NewMemoryStore())
}

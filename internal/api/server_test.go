package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Aritra7/cloudify-platform/internal/migrations"
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

func newTestServer() *Server {
	return NewServer(migrations.NewService(migrations.NewMemoryStore()))
}

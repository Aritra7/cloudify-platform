package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewClientRejectsUnsafeEndpoint(t *testing.T) {
	t.Parallel()
	for _, endpoint := range []string{
		"example.com",
		"http://example.com",
		"https://user:password@example.com",
		"https://example.com?token=secret",
	} {
		if _, err := NewClient(endpoint, "", "", nil); err == nil {
			t.Fatalf("NewClient(%q) succeeded", endpoint)
		}
	}
	if _, err := NewClient("http://127.0.0.1:8080", "", "", nil); err != nil {
		t.Fatalf("loopback endpoint rejected: %v", err)
	}
}

func TestClientCreatesMigrationWithAuthenticationAndIdempotency(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/v1/migrations" {
			t.Fatalf("request = %s %s", request.Method, request.URL.Path)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer provider-token" {
			t.Fatalf("authorization = %q", got)
		}
		if got := request.Header.Get("Idempotency-Key"); got != "application-main" {
			t.Fatalf("idempotency key = %q", got)
		}
		if got := request.Header.Get("User-Agent"); got != "terraform-provider-cloudify/test" {
			t.Fatalf("user agent = %q", got)
		}
		var payload CreateMigrationRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if payload.Source.RepositoryURL != "https://github.com/example/application" || payload.Destination.ProjectID != "example-project" {
			t.Fatalf("payload = %#v", payload)
		}
		response.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(response).Encode(testMigration("queued"))
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "provider-token", "terraform-provider-cloudify/test", server.Client())
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	migration, err := client.CreateMigration(context.Background(), CreateMigrationRequest{
		Source:      Source{RepositoryURL: "https://github.com/example/application", Revision: "main"},
		Destination: Destination{Provider: "gcp", ProjectID: "example-project", Region: "us-central1", Runtime: "cloud-run"},
	}, "application-main")
	if err != nil || migration.ID != "migration-1" {
		t.Fatalf("create migration = (%#v, %v)", migration, err)
	}
}

func TestClientWaitsForTerminalMigration(t *testing.T) {
	t.Parallel()
	var reads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		status := "running"
		if reads.Add(1) >= 2 {
			status = "succeeded"
		}
		_ = json.NewEncoder(response).Encode(testMigration(status))
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "", "", server.Client())
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	client.pollInterval = time.Millisecond
	migration, err := client.WaitMigration(context.Background(), "migration-1")
	if err != nil || migration.Status != "succeeded" || reads.Load() != 2 {
		t.Fatalf("wait migration = (%#v, %v), reads = %d", migration, err, reads.Load())
	}
}

func TestClientReturnsStructuredAPIError(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusConflict)
		_, _ = response.Write([]byte(`{"error":{"code":"invalid_state","message":"cannot cancel"}}`))
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "", "", server.Client())
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	_, err = client.CancelMigration(context.Background(), "migration-1")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusConflict || apiErr.Code != "invalid_state" {
		t.Fatalf("error = %#v", err)
	}
}

func testMigration(status string) Migration {
	return Migration{
		ID: "migration-1", Status: status,
		Source:      Source{RepositoryURL: "https://github.com/example/application", Revision: "main"},
		Destination: Destination{Provider: "gcp", ProjectID: "example-project", Region: "us-central1", Runtime: "cloud-run"},
		CreatedAt:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 1, 1, 0, 1, 0, 0, time.UTC),
	}
}

package observability

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Aritra7/cloudify-platform/internal/auth"
)

func TestRequestLoggerCapturesSafeRequestMetadata(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	handler := RequestLogger(logger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no", http.StatusTeapot)
	}))
	request := httptest.NewRequest(http.MethodPost, "/v1/resources?token=must-not-log", nil)
	request.Header.Set(requestIDHeader, "request-123")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusTeapot || response.Header().Get(requestIDHeader) != "request-123" {
		t.Fatalf("response = status %d, request ID %q", response.Code, response.Header().Get(requestIDHeader))
	}
	var record map[string]any
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatalf("decode log: %v", err)
	}
	if record["method"] != http.MethodPost || record["path"] != "/v1/resources" || record["status"] != float64(http.StatusTeapot) {
		t.Fatalf("log record = %#v", record)
	}
	if bytes.Contains(output.Bytes(), []byte("must-not-log")) {
		t.Fatalf("query string leaked into log: %s", output.String())
	}
}

func TestRequestLoggerReplacesUnsafeRequestID(t *testing.T) {
	t.Parallel()
	handler := RequestLogger(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	request.Header.Set(requestIDHeader, "unsafe\nvalue")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if value := response.Header().Get(requestIDHeader); value == "" || value == "unsafe\nvalue" {
		t.Fatalf("request ID was not replaced: %q", value)
	}
}

func TestStatusWriterCanBeUnwrappedForStreaming(t *testing.T) {
	t.Parallel()
	response := httptest.NewRecorder()
	writer := &statusWriter{ResponseWriter: response, status: http.StatusOK}
	if err := http.NewResponseController(writer).Flush(); err != nil {
		t.Fatalf("flush wrapped response: %v", err)
	}
}

func TestRequestLoggerCorrelatesAuthenticatedActor(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	digest := sha256.Sum256([]byte("test-token"))
	authenticator, err := auth.NewBearerAuthenticator(fmt.Sprintf(
		`[{"actor":"terraform-ci","token_sha256":"%x","roles":["planner"]}]`, digest,
	))
	if err != nil {
		t.Fatalf("create authenticator: %v", err)
	}
	inner := authenticator.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	handler := RequestLogger(slog.New(slog.NewJSONHandler(&output, nil)))(inner)
	request := httptest.NewRequest(http.MethodGet, "/v1/resources", nil)
	request.Header.Set("Authorization", "Bearer test-token")
	handler.ServeHTTP(httptest.NewRecorder(), request)

	var record map[string]any
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatalf("decode log: %v", err)
	}
	if record["actor"] != "terraform-ci" {
		t.Fatalf("actor = %#v, log = %s", record["actor"], output.String())
	}
}

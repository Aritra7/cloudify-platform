package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Aritra7/cloudify-platform/internal/events"
	"github.com/Aritra7/cloudify-platform/internal/iac"
	"github.com/Aritra7/cloudify-platform/internal/migrations"
	"github.com/Aritra7/cloudify-platform/internal/observability"
	"github.com/Aritra7/cloudify-platform/internal/plans"
)

const maxRequestBodyBytes = 1 << 20

// Server owns the HTTP surface of the Cloudify control plane.
type Server struct {
	handler    http.Handler
	migrations *migrations.Service
	events     events.Store
	metrics    *observability.Metrics
	plans      *plans.Service
}

// NewServer constructs an API server with operational endpoints.
func NewServer(migrationService *migrations.Service, eventStore events.Store, metricSets ...*observability.Metrics) *Server {
	metricSet := &observability.Metrics{}
	if len(metricSets) > 0 && metricSets[0] != nil {
		metricSet = metricSets[0]
	}
	return NewServerWithPlans(migrationService, eventStore, nil, metricSet)
}

// NewServerWithPlans constructs the complete API including Terraform planning.
func NewServerWithPlans(
	migrationService *migrations.Service,
	eventStore events.Store,
	planService *plans.Service,
	metricSet *observability.Metrics,
) *Server {
	if metricSet == nil {
		metricSet = &observability.Metrics{}
	}
	server := &Server{migrations: migrationService, events: eventStore, metrics: metricSet, plans: planService}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", health)
	mux.HandleFunc("GET /readyz", ready)
	mux.HandleFunc("GET /metrics", server.prometheusMetrics)
	mux.HandleFunc("POST /v1/migrations", server.createMigration)
	mux.HandleFunc("GET /v1/migrations/{id}", server.getMigration)
	mux.HandleFunc("POST /v1/migrations/{id}/cancel", server.cancelMigration)
	mux.HandleFunc("POST /v1/migrations/{id}/retry", server.retryMigration)
	mux.HandleFunc("GET /v1/migrations/{id}/attempts", server.listMigrationAttempts)
	mux.HandleFunc("GET /v1/migrations/{id}/events", server.listMigrationEvents)
	mux.HandleFunc("GET /v1/migrations/{id}/events/stream", server.streamMigrationEvents)
	if planService != nil {
		mux.HandleFunc("POST /v1/migrations/{id}/plans", server.createTerraformPlan)
		mux.HandleFunc("GET /v1/plans/{id}", server.getTerraformPlan)
		mux.HandleFunc("POST /v1/plans/{id}/approve", server.approveTerraformPlan)
	}
	server.handler = mux

	return server
}

func (s *Server) createTerraformPlan(w http.ResponseWriter, request *http.Request) {
	migrationID := request.PathValue("id")
	migration, err := s.migrations.Get(request.Context(), migrationID)
	if errors.Is(err, migrations.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not read migration")
		return
	}
	if migration.Status != migrations.StatusSucceeded {
		writeError(w, http.StatusConflict, "invalid_state", "Terraform planning requires a succeeded migration")
		return
	}
	idempotencyKey := strings.TrimSpace(request.Header.Get("Idempotency-Key"))
	if idempotencyKey == "" || len(idempotencyKey) > 128 {
		writeError(w, http.StatusBadRequest, "invalid_idempotency_key", "Idempotency-Key is required and must not exceed 128 characters")
		return
	}
	var specification iac.DeploymentSpec
	if err := decodeJSON(w, request, &specification); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if specification.MigrationID != "" && specification.MigrationID != migrationID {
		writeError(w, http.StatusUnprocessableEntity, "validation_failed", "specification migration_id must match the URL")
		return
	}
	plan, created, err := s.plans.Create(request.Context(), migrationID, idempotencyKey, specification)
	switch {
	case errors.Is(err, plans.ErrIdempotency):
		writeError(w, http.StatusConflict, "idempotency_conflict", err.Error())
	case err != nil:
		writeError(w, http.StatusUnprocessableEntity, "validation_failed", err.Error())
	default:
		w.Header().Set("Location", "/v1/plans/"+plan.ID)
		if !created {
			w.Header().Set("Idempotent-Replayed", "true")
			writeJSON(w, http.StatusOK, plan)
		} else if plan.Status == plans.StatusRejected {
			writeJSON(w, http.StatusCreated, plan)
		} else {
			writeJSON(w, http.StatusAccepted, plan)
		}
	}
}

func (s *Server) getTerraformPlan(w http.ResponseWriter, request *http.Request) {
	plan, err := s.plans.Get(request.Context(), request.PathValue("id"))
	if errors.Is(err, plans.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not read Terraform plan")
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

func (s *Server) approveTerraformPlan(w http.ResponseWriter, request *http.Request) {
	plan, err := s.plans.Approve(request.Context(), request.PathValue("id"), request.Header.Get("X-Cloudify-Actor"))
	switch {
	case errors.Is(err, plans.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, plans.ErrInvalidTransition):
		writeError(w, http.StatusConflict, "invalid_state", "only a policy-compliant ready plan can be approved")
	case err != nil:
		writeError(w, http.StatusBadRequest, "invalid_approval", err.Error())
	default:
		writeJSON(w, http.StatusOK, plan)
	}
}

func (s *Server) prometheusMetrics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = s.metrics.WritePrometheus(w)
}

func (s *Server) retryMigration(w http.ResponseWriter, request *http.Request) {
	migration, err := s.migrations.Retry(request.Context(), request.PathValue("id"))
	switch {
	case errors.Is(err, migrations.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, migrations.ErrInvalidTransition):
		writeError(w, http.StatusConflict, "invalid_state", "only failed migrations can be retried")
	case err != nil:
		writeError(w, http.StatusInternalServerError, "internal_error", "could not retry migration")
	default:
		writeJSON(w, http.StatusAccepted, migration)
	}
}

func (s *Server) listMigrationAttempts(w http.ResponseWriter, request *http.Request) {
	attempts, err := s.migrations.ListAttempts(request.Context(), request.PathValue("id"))
	if errors.Is(err, migrations.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not read migration attempts")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"attempts": attempts})
}

func (s *Server) listMigrationEvents(w http.ResponseWriter, request *http.Request) {
	migrationID := request.PathValue("id")
	if _, err := s.migrations.Get(request.Context(), migrationID); errors.Is(err, migrations.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", err.Error())
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not read migration")
		return
	}

	after, err := eventCursor(request)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_cursor", err.Error())
		return
	}
	eventList, err := s.events.ListAfter(request.Context(), migrationID, after, 200)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not read migration events")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": eventList})
}

func (s *Server) streamMigrationEvents(w http.ResponseWriter, request *http.Request) {
	migrationID := request.PathValue("id")
	migration, err := s.migrations.Get(request.Context(), migrationID)
	if errors.Is(err, migrations.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not read migration")
		return
	}
	cursor, err := eventCursor(request)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_cursor", err.Error())
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming_unsupported", "response streaming is unavailable")
		return
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	pollTicker := time.NewTicker(500 * time.Millisecond)
	defer pollTicker.Stop()
	keepaliveTicker := time.NewTicker(15 * time.Second)
	defer keepaliveTicker.Stop()
	for {
		eventList, err := s.events.ListAfter(request.Context(), migrationID, cursor, 200)
		if err != nil {
			_, _ = fmt.Fprint(w, "event: error\ndata: {\"message\":\"event stream failed\"}\n\n")
			flusher.Flush()
			return
		}
		for _, event := range eventList {
			data, err := json.Marshal(event)
			if err != nil {
				return
			}
			_, _ = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", event.Sequence, event.Kind, data)
			cursor = event.Sequence
		}
		if len(eventList) > 0 {
			flusher.Flush()
			continue
		}

		migration, err = s.migrations.Get(request.Context(), migrationID)
		if err != nil || migrations.IsTerminal(migration.Status) {
			return
		}
		select {
		case <-request.Context().Done():
			return
		case <-pollTicker.C:
		case <-keepaliveTicker.C:
			_, _ = fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

func eventCursor(request *http.Request) (int64, error) {
	value := request.URL.Query().Get("after")
	if value == "" {
		value = request.Header.Get("Last-Event-ID")
	}
	if value == "" {
		return 0, nil
	}
	cursor, err := strconv.ParseInt(value, 10, 64)
	if err != nil || cursor < 0 {
		return 0, errors.New("event cursor must be a non-negative integer")
	}
	return cursor, nil
}

// Handler exposes the server as an http.Handler for production and tests.
func (s *Server) Handler() http.Handler {
	return s.handler
}

type statusResponse struct {
	Status string `json:"status"`
}

func health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, statusResponse{Status: "ok"})
}

func ready(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, statusResponse{Status: "ready"})
}

func (s *Server) createMigration(w http.ResponseWriter, request *http.Request) {
	idempotencyKey := strings.TrimSpace(request.Header.Get("Idempotency-Key"))
	if idempotencyKey == "" || len(idempotencyKey) > 128 {
		writeError(w, http.StatusBadRequest, "invalid_idempotency_key", "Idempotency-Key is required and must not exceed 128 characters")
		return
	}

	var payload migrations.CreateRequest
	if err := decodeJSON(w, request, &payload); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := validateCreateRequest(payload); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "validation_failed", err.Error())
		return
	}

	migration, created, err := s.migrations.Create(request.Context(), idempotencyKey, payload)
	if errors.Is(err, migrations.ErrIdempotencyConflict) {
		writeError(w, http.StatusConflict, "idempotency_conflict", err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not create migration")
		return
	}

	w.Header().Set("Location", "/v1/migrations/"+migration.ID)
	if created {
		writeJSON(w, http.StatusAccepted, migration)
		return
	}
	w.Header().Set("Idempotent-Replayed", "true")
	writeJSON(w, http.StatusOK, migration)
}

func (s *Server) getMigration(w http.ResponseWriter, request *http.Request) {
	migration, err := s.migrations.Get(request.Context(), request.PathValue("id"))
	if errors.Is(err, migrations.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not read migration")
		return
	}
	writeJSON(w, http.StatusOK, migration)
}

func (s *Server) cancelMigration(w http.ResponseWriter, request *http.Request) {
	migration, err := s.migrations.Cancel(request.Context(), request.PathValue("id"))
	switch {
	case errors.Is(err, migrations.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, migrations.ErrInvalidTransition):
		writeError(w, http.StatusConflict, "invalid_state", "migration cannot be cancelled from its current state")
	case err != nil:
		writeError(w, http.StatusInternalServerError, "internal_error", "could not cancel migration")
	default:
		writeJSON(w, http.StatusOK, migration)
	}
}

func validateCreateRequest(request migrations.CreateRequest) error {
	repositoryURL, err := url.ParseRequestURI(request.Source.RepositoryURL)
	if err != nil || repositoryURL.Scheme != "https" || repositoryURL.Host == "" || repositoryURL.User != nil {
		return errors.New("source.repository_url must be an absolute HTTPS URL without embedded credentials")
	}
	if strings.TrimSpace(request.Source.Revision) == "" {
		return errors.New("source.revision is required")
	}
	if request.Destination.Provider != "gcp" {
		return errors.New("destination.provider must be gcp")
	}
	if strings.TrimSpace(request.Destination.ProjectID) == "" {
		return errors.New("destination.project_id is required")
	}
	if strings.TrimSpace(request.Destination.Region) == "" {
		return errors.New("destination.region is required")
	}
	if request.Destination.Runtime != "cloud-run" {
		return errors.New("destination.runtime must be cloud-run")
	}
	return nil
}

func decodeJSON(w http.ResponseWriter, request *http.Request, destination any) error {
	request.Body = http.MaxBytesReader(w, request.Body, maxRequestBodyBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain exactly one JSON value")
	}
	return nil
}

type errorResponse struct {
	Error apiError `json:"error"`
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorResponse{Error: apiError{Code: code, Message: message}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

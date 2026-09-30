package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/Aritra7/cloudify-platform/internal/migrations"
)

const maxRequestBodyBytes = 1 << 20

// Server owns the HTTP surface of the Cloudify control plane.
type Server struct {
	handler    http.Handler
	migrations *migrations.Service
}

// NewServer constructs an API server with operational endpoints.
func NewServer(migrationService *migrations.Service) *Server {
	server := &Server{migrations: migrationService}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", health)
	mux.HandleFunc("GET /readyz", ready)
	mux.HandleFunc("POST /v1/migrations", server.createMigration)
	mux.HandleFunc("GET /v1/migrations/{id}", server.getMigration)
	mux.HandleFunc("POST /v1/migrations/{id}/cancel", server.cancelMigration)
	server.handler = mux

	return server
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

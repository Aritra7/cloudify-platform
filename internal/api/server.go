package api

import (
	"encoding/json"
	"net/http"
)

// Server owns the HTTP surface of the Cloudify control plane.
type Server struct {
	handler http.Handler
}

// NewServer constructs an API server with operational endpoints.
func NewServer() *Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", health)
	mux.HandleFunc("GET /readyz", ready)

	return &Server{handler: mux}
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

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

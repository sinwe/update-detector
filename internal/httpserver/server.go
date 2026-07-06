// Package httpserver exposes the detection result over HTTP for Gatus (or
// anything else) to poll.
package httpserver

import (
	"encoding/json"
	"net/http"
	"sync"

	"update-detector/internal/checker"
	"update-detector/openapi"
)

type Server struct {
	mu     sync.RWMutex
	status *checker.Status

	mux *http.ServeMux
}

func New() *Server {
	s := &Server{mux: http.NewServeMux()}
	s.mux.HandleFunc("/status", s.handleStatus)
	s.mux.HandleFunc("/healthz", s.handleHealthz)
	s.mux.HandleFunc("/openapi.yaml", s.handleOpenAPISpec)
	return s
}

// Handler returns the http.Handler to serve, e.g. via http.Server.
func (s *Server) Handler() http.Handler { return s.mux }

// SetStatus updates the status served at /status. Safe for concurrent use.
func (s *Server) SetStatus(status checker.Status) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = &status
}

// handleStatus serves the full detection result as JSON — this is the URL
// to point a Gatus endpoint at, e.g. with a condition of
// "[BODY].ok == true".
func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	s.mu.RLock()
	status := s.status
	s.mu.RUnlock()

	if status == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"no check has completed yet"}`))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(status); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// handleHealthz reports process liveness only, independent of whether a
// detection cycle has succeeded — Gatus (or Docker's own healthcheck)
// shouldn't confuse "container is up" with "host is fully patched".
func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

// handleOpenAPISpec serves the OpenAPI 3.0 spec for this API (openapi/update-detector.yaml).
func (s *Server) handleOpenAPISpec(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/yaml")
	_, _ = w.Write(openapi.UpdateDetectorSpec)
}

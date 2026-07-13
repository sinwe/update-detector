// Package httpserver exposes the detection result over HTTP for Gatus (or
// anything else) to poll.
package httpserver

import (
	"encoding/json"
	"net/http"
	"sync"

	"update-detector/internal/checker"
	"update-detector/internal/version"
	"update-detector/openapi"
)

type Server struct {
	mu     sync.RWMutex
	status *checker.Status

	mux     *http.ServeMux
	recheck chan struct{}
}

func New() *Server {
	s := &Server{mux: http.NewServeMux(), recheck: make(chan struct{}, 1)}
	s.mux.HandleFunc("/status", s.handleStatus)
	s.mux.HandleFunc("/healthz", s.handleHealthz)
	s.mux.HandleFunc("/openapi.yaml", s.handleOpenAPISpec)
	s.mux.HandleFunc("/recheck", s.handleRecheck)
	return s
}

// Handler returns the http.Handler to serve, e.g. via http.Server.
func (s *Server) Handler() http.Handler { return s.mux }

// Recheck receives a value whenever POST /recheck is called, so the main
// detection loop can run an extra out-of-band cycle -- e.g. the companion
// calls this right after successfully applying an update, so the
// dashboard doesn't keep showing an already-applied package as pending
// for up to a full CHECK_INTERVAL. Buffered by 1: a request arriving
// while one's already queued just coalesces with it instead of blocking
// or being dropped.
func (s *Server) Recheck() <-chan struct{} {
	return s.recheck
}

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
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "version": version.Version})
}

// handleRecheck queues an out-of-band detection cycle and returns
// immediately -- the actual check (up to 5 minutes, per its own internal
// timeout) runs asynchronously on the main loop, not on this request.
func (s *Server) handleRecheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	select {
	case s.recheck <- struct{}{}:
	default:
		// One's already queued -- this request coalesces with it rather
		// than blocking or being silently dropped.
	}
	w.WriteHeader(http.StatusAccepted)
}

// handleOpenAPISpec serves the OpenAPI 3.0 spec for this API (openapi/update-detector.yaml).
func (s *Server) handleOpenAPISpec(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/yaml")
	_, _ = w.Write(openapi.UpdateDetectorSpec)
}

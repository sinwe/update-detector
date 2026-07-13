package httpserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"update-detector/internal/checker"
)

func TestHandleStatusBeforeFirstCheck(t *testing.T) {
	s := New()
	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("got status %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestHandleStatusAfterSetStatus(t *testing.T) {
	s := New()
	s.SetStatus(checker.Status{Hostname: "web01", OK: true})

	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("got Content-Type %q, want application/json", got)
	}
}

func TestHandleHealthz(t *testing.T) {
	s := New()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestHandleRecheck(t *testing.T) {
	s := New()
	req := httptest.NewRequest(http.MethodPost, "/recheck", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("got status %d, want %d", rec.Code, http.StatusAccepted)
	}

	select {
	case <-s.Recheck():
	default:
		t.Fatal("expected a value on Recheck() after POST /recheck")
	}
}

func TestHandleRecheckRejectsNonPost(t *testing.T) {
	s := New()
	req := httptest.NewRequest(http.MethodGet, "/recheck", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("got status %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestHandleRecheckCoalesces(t *testing.T) {
	s := New()
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodPost, "/recheck", nil)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("request %d: got status %d, want %d", i, rec.Code, http.StatusAccepted)
		}
	}

	// Three requests before anyone reads the channel coalesce into the one
	// buffered slot, not three queued values.
	<-s.Recheck()
	select {
	case <-s.Recheck():
		t.Fatal("expected only one coalesced value on Recheck(), got a second")
	default:
	}
}

func TestHandleOpenAPISpec(t *testing.T) {
	s := New()
	req := httptest.NewRequest(http.MethodGet, "/openapi.yaml", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/yaml" {
		t.Fatalf("got Content-Type %q, want application/yaml", got)
	}
	if !strings.Contains(rec.Body.String(), "openapi: 3.0.3") {
		t.Fatalf("body doesn't look like an OpenAPI spec: %s", rec.Body.String())
	}
}

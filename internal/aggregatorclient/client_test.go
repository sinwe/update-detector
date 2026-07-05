package aggregatorclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"update-detector/internal/checker"
)

func TestLoadOrCreateIdentityGeneratesAndPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity.json")

	id1, err := LoadOrCreateIdentity(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id1.AgentID == "" || id1.Token == "" {
		t.Fatalf("expected non-empty identity, got %#v", id1)
	}

	id2, err := LoadOrCreateIdentity(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id2 != id1 {
		t.Fatalf("expected reloaded identity to match, got %#v vs %#v", id2, id1)
	}
}

func TestLoadOrCreateIdentityUniquePerPath(t *testing.T) {
	dir := t.TempDir()
	id1, err := LoadOrCreateIdentity(filepath.Join(dir, "a.json"))
	if err != nil {
		t.Fatal(err)
	}
	id2, err := LoadOrCreateIdentity(filepath.Join(dir, "b.json"))
	if err != nil {
		t.Fatal(err)
	}
	if id1.AgentID == id2.AgentID || id1.Token == id2.Token {
		t.Fatalf("expected distinct identities, got %#v and %#v", id1, id2)
	}
}

func TestClientEnroll(t *testing.T) {
	var gotAgentID, gotHostname, gotToken string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req enrollRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		gotAgentID, gotHostname, gotToken = req.AgentID, req.Hostname, req.Token
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "pending"})
	}))
	defer srv.Close()

	client := New(srv.URL, Identity{AgentID: "agent-1", Token: "tok"})
	status, err := client.Enroll(context.Background(), "web01")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != "pending" {
		t.Fatalf("got status %q, want pending", status)
	}
	if gotAgentID != "agent-1" || gotHostname != "web01" || gotToken != "tok" {
		t.Fatalf("unexpected request fields: agent_id=%q hostname=%q token=%q", gotAgentID, gotHostname, gotToken)
	}
}

func TestClientReportSuccessAndFailure(t *testing.T) {
	var gotHeader http.Header
	var gotBody checker.Status
	statusCode := http.StatusOK

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(statusCode)
	}))
	defer srv.Close()

	client := New(srv.URL, Identity{AgentID: "agent-1", Token: "tok"})

	if err := client.Report(context.Background(), checker.Status{Hostname: "web01", OK: true}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotHeader.Get("X-Agent-ID") != "agent-1" || gotHeader.Get("Authorization") != "Bearer tok" {
		t.Fatalf("unexpected headers: %v", gotHeader)
	}
	if gotBody.Hostname != "web01" {
		t.Fatalf("unexpected body: %#v", gotBody)
	}

	statusCode = http.StatusForbidden
	if err := client.Report(context.Background(), checker.Status{Hostname: "web01"}); err == nil {
		t.Fatal("expected error on non-200 response")
	}
}

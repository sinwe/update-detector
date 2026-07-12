package companion

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"update-detector/internal/aggregator"
	"update-detector/internal/aggregatorclient"
)

func TestReportResult(t *testing.T) {
	var gotHeader http.Header
	var gotBody aggregator.ActionResult
	statusCode := http.StatusOK

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(statusCode)
	}))
	defer srv.Close()

	identity := aggregatorclient.Identity{AgentID: "agent-1", Token: "tok"}
	result := aggregator.ActionResult{ActionID: "act1", Success: true, Message: "done"}

	if err := ReportResult(context.Background(), srv.URL, identity, result); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotHeader.Get("X-Agent-ID") != "agent-1" || gotHeader.Get("Authorization") != "Bearer tok" {
		t.Fatalf("unexpected headers: %v", gotHeader)
	}
	if gotBody.ActionID != "act1" || !gotBody.Success || gotBody.Message != "done" {
		t.Fatalf("unexpected body: %#v", gotBody)
	}

	statusCode = http.StatusForbidden
	if err := ReportResult(context.Background(), srv.URL, identity, result); err == nil {
		t.Fatal("expected error on non-200 response")
	}
}

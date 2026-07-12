package companion

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"update-detector/internal/aggregator"
	"update-detector/internal/aggregatorclient"
)

func TestStreamRunInvokesOnAction(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Agent-ID") != "agent-1" || r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("unexpected headers: %v", r.Header)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)

		// A malformed frame must be ignored, not crash the stream.
		fmt.Fprint(w, "data: not-json\n\n")
		flusher.Flush()

		action := aggregator.Action{ID: "act1", Type: aggregator.ActionUpgrade, CreatedAt: time.Now()}
		payload, _ := json.Marshal(action)
		fmt.Fprintf(w, "data: %s\n\n", payload)
		flusher.Flush()
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	received := make(chan aggregator.Action, 1)
	go StreamRun(ctx, srv.URL, aggregatorclient.Identity{AgentID: "agent-1", Token: "tok"}, func(a aggregator.Action) {
		received <- a
	})

	select {
	case got := <-received:
		if got.ID != "act1" || got.Type != aggregator.ActionUpgrade {
			t.Fatalf("got action %#v, want id act1/upgrade", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for action")
	}
}

func TestStreamRunStopsOnContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		StreamRun(ctx, srv.URL, aggregatorclient.Identity{AgentID: "a", Token: "t"}, func(aggregator.Action) {})
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("StreamRun did not exit promptly after context cancellation")
	}
}

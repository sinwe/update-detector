package agentstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"update-detector/internal/aggregator"
	"update-detector/internal/aggregatorclient"
)

func TestRunInvokesOnAction(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Agent-ID") != "agent-1" || r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("unexpected headers: %v", r.Header)
		}
		if got := r.Header.Get("X-Client-Kind"); got != "companion" {
			t.Errorf("got X-Client-Kind %q, want companion", got)
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
	go Run(ctx, srv.URL, aggregatorclient.Identity{AgentID: "agent-1", Token: "tok"}, aggregator.KindCompanion, false, false, func(a aggregator.Action) {
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

func TestRunStopsOnContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		Run(ctx, srv.URL, aggregatorclient.Identity{AgentID: "a", Token: "t"}, aggregator.KindAgent, false, false, func(aggregator.Action) {})
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit promptly after context cancellation")
	}
}

func TestRunOnceSendsRequestedKind(t *testing.T) {
	var gotKind string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKind = r.Header.Get("X-Client-Kind")
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := runOnce(ctx, srv.URL, aggregatorclient.Identity{AgentID: "a", Token: "t"}, aggregator.KindAgent, false, false, func(aggregator.Action) {}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotKind != "agent" {
		t.Fatalf("got X-Client-Kind %q, want agent", gotKind)
	}
}

func TestRunOnce409YieldsErrSuperseded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "superseded", http.StatusConflict)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := runOnce(ctx, srv.URL, aggregatorclient.Identity{AgentID: "a", Token: "t"}, aggregator.KindAgent, false, false, func(aggregator.Action) {})
	if !errors.Is(err, errSuperseded) {
		t.Fatalf("got err %v, want errSuperseded for a 409 response", err)
	}
}

func TestRunOnceSupersededFrameYieldsErrSuperseded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		fmt.Fprint(w, "event: superseded\ndata: {}\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var calledOnAction bool
	err := runOnce(ctx, srv.URL, aggregatorclient.Identity{AgentID: "a", Token: "t"}, aggregator.KindAgent, false, false, func(aggregator.Action) {
		calledOnAction = true
	})
	if !errors.Is(err, errSuperseded) {
		t.Fatalf("got err %v, want errSuperseded for a superseded SSE frame", err)
	}
	if calledOnAction {
		t.Fatal("a superseded frame's data must not be parsed as an Action")
	}
}

func TestRunOnceEventLineResetsOnBlankLine(t *testing.T) {
	// A "data:" line in a frame with no "event:" line of its own must
	// never be misattributed to some earlier frame's event name -- the
	// blank line ending the previous frame (here, a heartbeat comment)
	// must reset the tracked event back to none.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)

		action := aggregator.Action{ID: "act1", Type: aggregator.ActionRecheck, CreatedAt: time.Now()}
		payload, _ := json.Marshal(action)
		fmt.Fprint(w, ": heartbeat\n\n")
		fmt.Fprintf(w, "data: %s\n\n", payload)
		flusher.Flush()
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var got aggregator.Action
	err := runOnce(ctx, srv.URL, aggregatorclient.Identity{AgentID: "a", Token: "t"}, aggregator.KindAgent, false, false, func(a aggregator.Action) {
		got = a
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ID != "act1" {
		t.Fatalf("got action %#v, want id act1 -- the data frame must be parsed normally, not misattributed to a stale event", got)
	}
}

func TestNextRetryDelay(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		backoff  time.Duration
		wantWait time.Duration
		wantNext time.Duration
	}{
		{"clean run resets", nil, 16 * time.Second, time.Second, time.Second},
		{"superseded holds steady", errSuperseded, 5 * time.Second, supersededRetryInterval, 5 * time.Second},
		{"transient error grows", errors.New("boom"), 2 * time.Second, 2 * time.Second, 4 * time.Second},
		{"transient error caps", errors.New("boom"), maxStreamBackoff, maxStreamBackoff, maxStreamBackoff},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wait, next := nextRetryDelay(c.err, c.backoff)
			if wait != c.wantWait || next != c.wantNext {
				t.Fatalf("nextRetryDelay(%v, %s) = (%s, %s), want (%s, %s)",
					c.err, c.backoff, wait, next, c.wantWait, c.wantNext)
			}
		})
	}
}

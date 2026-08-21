package companion

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"update-detector/internal/aggregatorclient"
)

func TestStreamOutputDeliversLinesInOrder(t *testing.T) {
	var mu sync.Mutex
	var got []outputFrame

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("action_id") != "act1" {
			t.Errorf("got action_id query %q, want act1", r.URL.Query().Get("action_id"))
		}
		if r.Header.Get("X-Agent-ID") != "agent1" || r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("missing/wrong auth headers: %v", r.Header)
		}
		scanner := bufio.NewScanner(r.Body)
		for scanner.Scan() {
			var f outputFrame
			if err := json.Unmarshal(scanner.Bytes(), &f); err != nil {
				t.Errorf("bad frame: %v", err)
				continue
			}
			mu.Lock()
			got = append(got, f)
			mu.Unlock()
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sink := NewOutputSink(10)
	sink.Push("line one")
	sink.Push("line two")
	sink.Close()

	identity := aggregatorclient.Identity{AgentID: "agent1", Token: "tok"}
	if err := StreamOutput(context.Background(), srv.URL, identity, "act1", sink); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 || got[0].Line != "line one" || got[1].Line != "line two" {
		t.Fatalf("got %#v, want two lines in order", got)
	}
}

// TestStreamOutputReturnsPromptlyWhenAggregatorUnreachable is the
// regression test for the blocking-pipe gotcha: an OutputSink fed
// directly into an io.Pipe with nobody ever reading it (aggregator
// unreachable) must not hang StreamOutput -- and, by extension, must
// never hang the command producing the sink's lines either.
func TestStreamOutputReturnsPromptlyWhenAggregatorUnreachable(t *testing.T) {
	// Listen then immediately close, so the address is real but nothing
	// is listening -- connections fail fast (refused) rather than a slow
	// timeout, same trick execute_test.go's
	// TestApplyFailsWhenLocalStatusUnreachable uses.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	sink := NewOutputSink(10)
	go func() {
		sink.Push("line one")
		sink.Close()
	}()

	identity := aggregatorclient.Identity{AgentID: "agent1", Token: "tok"}
	done := make(chan error, 1)
	go func() {
		done <- StreamOutput(context.Background(), "http://"+addr, identity, "act1", sink)
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected an error when the aggregator is unreachable")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("StreamOutput hung instead of returning promptly when the aggregator is unreachable")
	}
}

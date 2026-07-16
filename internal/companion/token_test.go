//go:build !windows

package companion

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"update-detector/internal/aggregatorclient"
	"update-detector/internal/companiontoken"
)

func TestFetchIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "companion.sock")
	want := aggregatorclient.Identity{AgentID: "agent-1", Token: "secret-token"}

	srv, err := companiontoken.Listen(path, want)
	if err != nil {
		t.Fatalf("Listen failed: %v", err)
	}
	defer srv.Close()
	go srv.Serve()

	got, err := FetchIdentity(context.Background(), path)
	if err != nil {
		t.Fatalf("FetchIdentity failed: %v", err)
	}
	if got != want {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestFetchIdentityWithRetrySucceedsOnceSocketAppears(t *testing.T) {
	path := filepath.Join(t.TempDir(), "companion.sock")
	want := aggregatorclient.Identity{AgentID: "agent-1", Token: "secret-token"}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		got, err := FetchIdentityWithRetry(ctx, path, 50*time.Millisecond)
		if err != nil {
			t.Errorf("FetchIdentityWithRetry failed: %v", err)
			return
		}
		if got != want {
			t.Errorf("got %#v, want %#v", got, want)
		}
	}()

	// The socket doesn't exist yet -- FetchIdentityWithRetry must keep
	// retrying instead of giving up immediately.
	time.Sleep(100 * time.Millisecond)
	srv, err := companiontoken.Listen(path, want)
	if err != nil {
		t.Fatalf("Listen failed: %v", err)
	}
	defer srv.Close()
	go srv.Serve()

	<-done
}

func TestFetchIdentityWithRetryRespectsContextCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "never-created.sock")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	if _, err := FetchIdentityWithRetry(ctx, path, 10*time.Millisecond); err == nil {
		t.Fatal("expected an error once the context is done")
	}
}

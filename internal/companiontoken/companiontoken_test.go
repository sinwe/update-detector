package companiontoken

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"update-detector/internal/aggregatorclient"
)

func TestListenAndServe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "companion.sock")
	identity := aggregatorclient.Identity{AgentID: "agent-1", Token: "secret-token"}

	srv, err := Listen(path, identity)
	if err != nil {
		t.Fatalf("Listen failed: %v", err)
	}
	defer srv.Close()

	go srv.Serve()

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", path)
			},
		},
		Timeout: 2 * time.Second,
	}

	resp, err := client.Get("http://unix/companion/token")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("got status %d, want 200", resp.StatusCode)
	}
	var got aggregatorclient.Identity
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if got != identity {
		t.Fatalf("got %#v, want %#v", got, identity)
	}
}

func TestListenRemovesStaleSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "companion.sock")
	identity := aggregatorclient.Identity{AgentID: "a", Token: "t"}

	srv1, err := Listen(path, identity)
	if err != nil {
		t.Fatalf("first Listen failed: %v", err)
	}
	srv1.Close()

	// A second Listen at the same path should succeed by removing the
	// stale socket file left behind by the first (unclosed by the OS,
	// just orphaned) rather than failing with "address already in use".
	srv2, err := Listen(path, identity)
	if err != nil {
		t.Fatalf("second Listen failed: %v", err)
	}
	srv2.Close()
}

func TestRejectsNonGet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "companion.sock")
	srv, err := Listen(path, aggregatorclient.Identity{AgentID: "a", Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	go srv.Serve()

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", path)
			},
		},
		Timeout: 2 * time.Second,
	}

	resp, err := client.Post("http://unix/companion/token", "application/json", nil)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("got status %d, want 405", resp.StatusCode)
	}
}

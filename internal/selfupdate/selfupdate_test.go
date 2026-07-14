package selfupdate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func serverServing(t *testing.T, body string, status int, includePreRelease bool) (*httptest.Server, *Client) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, New(srv.URL, includePreRelease)
}

func TestRefreshStableChannelPicksHighestRealReleaseIgnoringPreReleasesAndOrder(t *testing.T) {
	// Deliberately out of order, and with an rc tag that's "newest" if
	// naively taking the first list entry -- highestRelease must scan
	// everything and compare numerically, not trust list order.
	body := `[
		{"tag_name": "v0.10.0-rc2"},
		{"tag_name": "v0.9.0"},
		{"tag_name": "v0.10.0"},
		{"tag_name": "v0.10.0-rc1"}
	]`
	_, c := serverServing(t, body, http.StatusOK, false)

	if err := c.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh failed: %v", err)
	}
	tag, _, ok := c.Latest()
	if !ok {
		t.Fatal("expected a cached result after a successful Refresh")
	}
	if tag != "v0.10.0" {
		t.Fatalf("got latest tag %q, want v0.10.0 (the real release, not the newer-looking -rc2)", tag)
	}
}

func TestRefreshPreReleaseChannelPrefersNewestRc(t *testing.T) {
	body := `[
		{"tag_name": "v0.10.0"},
		{"tag_name": "v0.10.0-rc2"},
		{"tag_name": "v0.10.0-rc1"}
	]`
	_, c := serverServing(t, body, http.StatusOK, true)

	if err := c.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh failed: %v", err)
	}
	tag, _, _ := c.Latest()
	if tag != "v0.10.0" {
		t.Fatalf("got tag %q, want v0.10.0 (the real release outranks any -rc of the same version)", tag)
	}
}

func TestRefreshPreReleaseChannelPicksRcNewerThanAnyRealRelease(t *testing.T) {
	body := `[{"tag_name": "v0.10.0"}, {"tag_name": "v0.11.0-rc1"}]`
	_, c := serverServing(t, body, http.StatusOK, true)

	if err := c.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh failed: %v", err)
	}
	tag, _, _ := c.Latest()
	if tag != "v0.11.0-rc1" {
		t.Fatalf("got tag %q, want v0.11.0-rc1 (a newer pre-release outranks an older real release on this channel)", tag)
	}
}

func TestRefreshFailurePreservesPreviousResult(t *testing.T) {
	srv, c := serverServing(t, `[{"tag_name": "v0.10.0"}]`, http.StatusOK, false)
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatalf("first Refresh failed: %v", err)
	}

	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	if err := c.Refresh(context.Background()); err == nil {
		t.Fatal("expected the second Refresh to fail (server now returns 500)")
	}
	tag, _, ok := c.Latest()
	if !ok || tag != "v0.10.0" {
		t.Fatalf("expected the previous result v0.10.0 to survive a failed refresh, got tag=%q ok=%v", tag, ok)
	}
}

func TestRefreshNoMatchingReleaseFound(t *testing.T) {
	_, c := serverServing(t, `[{"tag_name": "v0.10.0-rc1"}]`, http.StatusOK, false)
	if err := c.Refresh(context.Background()); err == nil {
		t.Fatal("expected an error when every release is a pre-release and the channel excludes them")
	}
	if _, _, ok := c.Latest(); ok {
		t.Fatal("expected no cached result before any successful Refresh")
	}
}

func TestRefreshSkipsUnparseableTagsWithoutFailingTheScan(t *testing.T) {
	body := `[{"tag_name": "some-unrelated-tag"}, {"tag_name": "v0.10.0"}]`
	_, c := serverServing(t, body, http.StatusOK, false)
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh failed: %v", err)
	}
	tag, _, _ := c.Latest()
	if tag != "v0.10.0" {
		t.Fatalf("got tag %q, want v0.10.0", tag)
	}
}

func TestHighestReleaseHelper(t *testing.T) {
	releases := []release{{TagName: "v1.0.0"}, {TagName: "v2.0.0-rc1"}, {TagName: "v1.5.0"}}

	tag, ok := highestRelease(releases, false)
	if !ok || tag != "v1.5.0" {
		t.Fatalf("stable channel: got tag=%q ok=%v, want v1.5.0 (v2.0.0-rc1 excluded)", tag, ok)
	}

	tag, ok = highestRelease(releases, true)
	if !ok || tag != "v2.0.0-rc1" {
		t.Fatalf("pre-release channel: got tag=%q ok=%v, want v2.0.0-rc1 (highest overall)", tag, ok)
	}
}

func TestRunRefreshesImmediatelyAndOnTimer(t *testing.T) {
	var reqCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqCount++
		_ = json.NewEncoder(w).Encode([]release{{TagName: "v0.10.0"}})
	}))
	defer srv.Close()
	c := New(srv.URL, false)

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	Run(ctx, c, 50*time.Millisecond, nil)

	if reqCount < 2 {
		t.Fatalf("got %d requests in 250ms with a 50ms interval, want at least 2 (one immediate + at least one tick)", reqCount)
	}
	if _, _, ok := c.Latest(); !ok {
		t.Fatal("expected Run to have populated a cached result")
	}
}

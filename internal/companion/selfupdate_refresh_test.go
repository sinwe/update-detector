//go:build !windows

package companion

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withInstallShRefresh points installShPath and installShRawBase at temp
// locations and restores both afterwards, so no test ever touches the
// network or the real cache.
func withInstallShRefresh(t *testing.T, base string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "install.sh")
	origPath, origBase := installShPath, installShRawBase
	installShPath, installShRawBase = path, base
	t.Cleanup(func() { installShPath, installShRawBase = origPath, origBase })
	return path
}

func TestRefreshInstallShReplacesStaleCache(t *testing.T) {
	script := "#!/bin/sh\n# fresh from the tag\necho hi\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v9.9.9/install.sh" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(script))
	}))
	defer srv.Close()

	path := withInstallShRefresh(t, srv.URL)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n# months old\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := refreshInstallSh(context.Background(), "v9.9.9"); err != nil {
		t.Fatalf("refreshInstallSh: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != script {
		t.Fatalf("cached script was not replaced: %q", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o755 {
		t.Fatalf("mode = %o, want 755", perm)
	}
}

func TestRefreshInstallShRejectsNonScriptAndKeepsCache(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A captive portal / proxy answering 200 with HTML.
		_, _ = w.Write([]byte("<html><body>login</body></html>"))
	}))
	defer srv.Close()

	path := withInstallShRefresh(t, srv.URL)
	const old = "#!/bin/sh\n# keep me\n"
	if err := os.WriteFile(path, []byte(old), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := refreshInstallSh(context.Background(), "v9.9.9"); err == nil {
		t.Fatal("expected an error for a non-script response")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != old {
		t.Fatalf("a bad response must not clobber the cache, got: %q", got)
	}
}

func TestRefreshInstallShErrorsOnMissingTag(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	withInstallShRefresh(t, srv.URL)
	if err := refreshInstallSh(context.Background(), "v9.9.9"); err == nil {
		t.Fatal("expected an error for a 404")
	}
	if err := refreshInstallSh(context.Background(), ""); err == nil ||
		!strings.Contains(err.Error(), "no target version") {
		t.Fatalf("expected the empty-target-version error, got %v", err)
	}
}

func TestInstallNativeStillRunsWhenRefreshFails(t *testing.T) {
	// The whole point of best-effort: an offline host must still update
	// from whatever script it already has cached.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	callLog := filepath.Join(t.TempDir(), "calls.log")
	script := "#!/bin/sh\necho ran >> \"" + callLog + "\"\nexit 0\n"
	path := withInstallShRefresh(t, srv.URL)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := installNative(context.Background(), "agent", "v9.9.9"); err != nil {
		t.Fatalf("installNative should tolerate a failed refresh: %v", err)
	}
	if _, err := os.Stat(callLog); err != nil {
		t.Fatalf("the cached script should still have run: %v", err)
	}
}

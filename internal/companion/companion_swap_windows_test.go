//go:build windows

package companion

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"update-detector/internal/aggregator"
)

// writeFakeSc creates a fake sc.bat that logs commands to callLog.
func writeFakeSc(t *testing.T, callLog string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "sc.bat")
	script := `@echo off
echo %* >> "` + callLog + `"
if "%1"=="query" (
  echo STATE              : 4  RUNNING
  timeout /t 1 /nobreak >nul
  echo STATE              : 1  STOPPED
)
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	// Prepend our fake sc dir to PATH
	t.Setenv("PATH", dir+";"+os.Getenv("PATH"))
}

func TestCompleteCompanionSwapSuccess(t *testing.T) {
	// Create a fake .exe.new file
	dir := t.TempDir()
	exePath := filepath.Join(dir, "update-detector-companion.exe")
	newPath := exePath + ".new"

	// Write a dummy .new file
	if err := os.WriteFile(newPath, []byte("fake companion binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Override companionExePath
	orig := companionExePath
	companionExePath = exePath
	t.Cleanup(func() { companionExePath = orig })

	callLog := filepath.Join(t.TempDir(), "sc-calls.log")
	writeFakeSc(t, callLog)

	action := aggregator.Action{ID: "act1", Type: aggregator.ActionCompleteCompanionSwap}
	result := CompleteCompanionSwap(context.Background(), action)

	if !result.Success {
		t.Fatalf("expected success, got: %s", result.Message)
	}

	// Verify .new was moved to the exe path
	if _, err := os.Stat(exePath); err != nil {
		t.Fatalf("expected companion exe to exist at %s: %v", exePath, err)
	}
	// Verify .new no longer exists
	if _, err := os.Stat(newPath); !os.IsNotExist(err) {
		t.Fatal("expected .new file to be gone after swap")
	}

	// Verify sc was called to stop and start
	got, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatal(err)
	}
	calls := string(got)
	if !strings.Contains(calls, "stop update-detector-companion") {
		t.Fatalf("expected sc stop call, got: %q", calls)
	}
	if !strings.Contains(calls, "start update-detector-companion") {
		t.Fatalf("expected sc start call, got: %q", calls)
	}
}

func TestCompleteCompanionSwapMissingNewFile(t *testing.T) {
	dir := t.TempDir()
	exePath := filepath.Join(dir, "update-detector-companion.exe")

	orig := companionExePath
	companionExePath = exePath
	t.Cleanup(func() { companionExePath = orig })

	action := aggregator.Action{ID: "act1", Type: aggregator.ActionCompleteCompanionSwap}
	result := CompleteCompanionSwap(context.Background(), action)

	if result.Success {
		t.Fatal("expected failure when .new file is missing")
	}
	if !strings.Contains(result.Message, "not found") {
		t.Fatalf("expected 'not found' in message, got: %s", result.Message)
	}
}

func TestCompleteCompanionSwapEmptyNewFile(t *testing.T) {
	dir := t.TempDir()
	exePath := filepath.Join(dir, "update-detector-companion.exe")
	newPath := exePath + ".new"

	// Write an empty .new file
	if err := os.WriteFile(newPath, []byte{}, 0o755); err != nil {
		t.Fatal(err)
	}

	orig := companionExePath
	companionExePath = exePath
	t.Cleanup(func() { companionExePath = orig })

	action := aggregator.Action{ID: "act1", Type: aggregator.ActionCompleteCompanionSwap}
	result := CompleteCompanionSwap(context.Background(), action)

	if result.Success {
		t.Fatal("expected failure when .new file is empty")
	}
	if !strings.Contains(result.Message, "empty") {
		t.Fatalf("expected 'empty' in message, got: %s", result.Message)
	}
	// Verify empty .new was cleaned up
	if _, err := os.Stat(newPath); !os.IsNotExist(err) {
		t.Fatal("expected empty .new file to be removed")
	}
}

func TestStageCompanionUpdateWritesNewFile(t *testing.T) {
	// Set up a fake GitHub API server that returns a release with a
	// downloadable asset.
	dir := t.TempDir()
	exePath := filepath.Join(dir, "update-detector-companion.exe")

	orig := companionExePath
	companionExePath = exePath
	t.Cleanup(func() { companionExePath = orig })

	// Create a fake HTTP server that serves both the release API and
	// the asset download.
	fakeBinary := []byte("fake companion binary v0.99.0")
	fakeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/repos/test/repo/releases/tags/v0.99.0":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"assets": []map[string]any{
					{
						"name":               "update-detector-companion-windows-amd64.exe",
						"browser_download_url": "http" + "://" + r.Host + "/download/companion.exe",
					},
				},
			})
		case "/download/companion.exe":
			w.Write(fakeBinary)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(fakeServer.Close)

	// Override GITHUB_API_BASE
	t.Setenv("GITHUB_API_BASE", fakeServer.URL+"/api/v1/repos/test/repo")

	action := aggregator.Action{ID: "act1", Type: aggregator.ActionSelfUpdate, Component: "companion", TargetVersion: "v0.99.0"}
	result := stageCompanionUpdate(context.Background(), action)

	if !result.Success {
		t.Fatalf("expected success, got: %s", result.Message)
	}
	if !result.Staged {
		t.Fatal("expected Staged=true in result")
	}

	// Verify .new file was written
	newPath := exePath + ".new"
	data, err := os.ReadFile(newPath)
	if err != nil {
		t.Fatalf("expected .new file at %s: %v", newPath, err)
	}
	if string(data) != string(fakeBinary) {
		t.Fatalf("expected downloaded binary content, got %d bytes", len(data))
	}

	// Verify .tmp file was cleaned up
	tmpPath := newPath + ".tmp"
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Fatal("expected .tmp file to be cleaned up")
	}
}

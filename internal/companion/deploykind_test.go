package companion

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// writeFakeDocker puts a fake "docker" script at the front of PATH for
// the duration of the test, mirroring writeFakeAptGet in execute_test.go.
func writeFakeDocker(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "docker")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func withSystemdUnitDir(t *testing.T, dir string) {
	t.Helper()
	orig := systemdUnitDir
	systemdUnitDir = dir
	t.Cleanup(func() { systemdUnitDir = orig })
}

func TestNativeUnitPresent(t *testing.T) {
	dir := t.TempDir()
	withSystemdUnitDir(t, dir)

	if nativeUnitPresent("update-detector") {
		t.Fatal("expected no unit present in an empty dir")
	}

	if err := os.WriteFile(filepath.Join(dir, "update-detector.service"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !nativeUnitPresent("update-detector") {
		t.Fatal("expected the unit to be present once its file exists")
	}
	// A stopped/disabled unit still has its file on disk -- this check
	// is deliberately just file existence, not systemctl is-active.
}

func TestDockerContainerForNoDockerOnPath(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // guarantees no "docker" binary anywhere on PATH
	id, image, err := dockerContainerFor(context.Background(), "update-detector")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "" || image != "" {
		t.Fatalf("got id %q image %q, want both empty when docker isn't on PATH", id, image)
	}
}

func TestDockerContainerForMatchesAnchored(t *testing.T) {
	writeFakeDocker(t, `
if [ "$1" = "ps" ]; then
  echo "aaa111 forgejo.winar.to/winarto/update-detector-companion:v0.9.0"
  echo "bbb222 forgejo.winar.to/winarto/update-detector:v0.9.0"
fi
`)
	id, image, err := dockerContainerFor(context.Background(), "update-detector")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "bbb222" {
		t.Fatalf("got id %q, want bbb222 -- must not match the -companion image for a bare \"update-detector\" pattern", id)
	}
	if image != "forgejo.winar.to/winarto/update-detector:v0.9.0" {
		t.Fatalf("got image %q, want the matching container's own image reference", image)
	}
}

func TestDockerContainerForNoMatch(t *testing.T) {
	writeFakeDocker(t, `
if [ "$1" = "ps" ]; then
  echo "aaa111 some-other-image:latest"
fi
`)
	id, image, err := dockerContainerFor(context.Background(), "update-detector")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "" || image != "" {
		t.Fatalf("got id %q image %q, want both empty for no matching image", id, image)
	}
}

func TestDetectKindAndAmbiguous(t *testing.T) {
	dir := t.TempDir()
	withSystemdUnitDir(t, dir)

	cases := []struct {
		name          string
		native        bool
		dockerID      string
		wantKind      DeployKind
		wantAmbiguous bool
	}{
		{"none", false, "", DeployNone, false},
		{"native only", true, "", DeployNative, false},
		{"docker only", false, "abc", DeployDocker, false},
		{"both", true, "abc", DeployNative, true}, // native takes precedence, but flagged ambiguous
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := Detection{Native: c.native, DockerContainerID: c.dockerID}
			if got := d.Kind(); got != c.wantKind {
				t.Errorf("Kind() = %v, want %v", got, c.wantKind)
			}
			if got := d.Ambiguous(); got != c.wantAmbiguous {
				t.Errorf("Ambiguous() = %v, want %v", got, c.wantAmbiguous)
			}
		})
	}
}

func TestDetectEndToEnd(t *testing.T) {
	dir := t.TempDir()
	withSystemdUnitDir(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "update-detector.service"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeFakeDocker(t, `
if [ "$1" = "ps" ]; then
  echo "aaa111 forgejo.winar.to/winarto/update-aggregator:v0.9.0"
fi
`)

	d, err := Detect(context.Background(), "update-detector")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !d.Native || d.DockerContainerID != "" {
		t.Fatalf("got %#v, want native-only for update-detector", d)
	}

	d, err = Detect(context.Background(), "update-aggregator")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.Native || d.DockerContainerID != "aaa111" {
		t.Fatalf("got %#v, want docker-only (id=aaa111) for update-aggregator", d)
	}
}

func TestDockerContainerForPropagatesRealError(t *testing.T) {
	writeFakeDocker(t, `exit 1`)
	if _, _, err := dockerContainerFor(context.Background(), "update-detector"); err == nil {
		t.Fatal("expected an error when docker itself exits non-zero")
	}
}

func TestNativeUnitPresentUsesExactName(t *testing.T) {
	dir := t.TempDir()
	withSystemdUnitDir(t, dir)
	if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%s.service", "update-detector-companion")), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if nativeUnitPresent("update-detector") {
		t.Fatal("update-detector-companion's unit file must not be mistaken for update-detector's own")
	}
}

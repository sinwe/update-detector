//go:build !windows

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
case "$1" in
  ps) echo "aaa111"; echo "bbb222" ;;
  inspect)
    shift 3
    for id in "$@"; do
      case "$id" in
        aaa111) echo "forgejo.winar.to/winarto/update-detector-companion:v0.9.0" ;;
        bbb222) echo "forgejo.winar.to/winarto/update-detector:v0.9.0" ;;
      esac
    done
    ;;
esac
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
case "$1" in
  ps) echo "aaa111" ;;
  inspect)
    shift 3
    for id in "$@"; do
      case "$id" in
        aaa111) echo "some-other-image:latest" ;;
      esac
    done
    ;;
esac
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
case "$1" in
  ps) echo "aaa111" ;;
  inspect)
    shift 3
    for id in "$@"; do
      case "$id" in
        aaa111) echo "forgejo.winar.to/winarto/update-aggregator:v0.9.0" ;;
      esac
    done
    ;;
esac
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

// TestDockerContainerForSurvivesTagDrift is the regression test for a
// real bug caught live: `docker ps --format '{{.Image}}'` silently falls
// back to printing a bare image ID once a container's original tag has
// been reassigned to a different image on the registry (e.g. a later
// `docker pull` under the same :latest this repo's own compose files
// pin, moved every time release.yml pushes a new tag). This confirmed
// live against a real long-running container: `docker ps` showed a raw
// hex ID while `docker inspect .Config.Image` still correctly reported
// its actual "...update-detector:latest" reference. Detection must use
// the latter, not the former, or it silently stops finding any container
// that hasn't been recreated since the tag last moved.
func TestDockerContainerForSurvivesTagDrift(t *testing.T) {
	writeFakeDocker(t, `
case "$1" in
  ps) echo "aaa111" ;;
  inspect)
    shift 3
    for id in "$@"; do
      case "$id" in
        # docker ps itself would show a bare ID like this for aaa111 --
        # dockerContainerFor must never ask docker ps for the image at
        # all, only docker inspect, which still resolves it correctly.
        aaa111) echo "forgejo.winar.to/winarto/update-detector:latest" ;;
      esac
    done
    ;;
esac
`)
	id, image, err := dockerContainerFor(context.Background(), "update-detector")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "aaa111" {
		t.Fatalf("got id %q, want aaa111", id)
	}
	if image != "forgejo.winar.to/winarto/update-detector:latest" {
		t.Fatalf("got image %q, want the tag docker inspect reports, not whatever docker ps would have shown", image)
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

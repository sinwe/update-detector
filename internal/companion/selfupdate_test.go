//go:build !windows

package companion

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"update-detector/internal/aggregator"
)

// writeFakeInstallSh puts a fake install.sh at installShPath for the
// duration of the test, logging INSTALL_COMPONENTS/INSTALL_VERSION to
// callLog (one line per invocation) and exiting with exitCode.
func writeFakeInstallSh(t *testing.T, callLog string, exitCode int) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "install.sh")
	script := `#!/bin/sh
echo "$INSTALL_COMPONENTS $INSTALL_VERSION" >> "` + callLog + `"
exit ` + strconv.Itoa(exitCode) + `
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	orig := installShPath
	installShPath = path
	t.Cleanup(func() { installShPath = orig })
}

func TestInstallNativeSetsEnvAndSucceeds(t *testing.T) {
	callLog := filepath.Join(t.TempDir(), "calls.log")
	writeFakeInstallSh(t, callLog, 0)

	if err := installNative(context.Background(), "agent", "v0.11.0"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "agent v0.11.0\n" {
		t.Fatalf("got call log %q, want %q", got, "agent v0.11.0\n")
	}
}

func TestInstallNativePropagatesFailure(t *testing.T) {
	writeFakeInstallSh(t, filepath.Join(t.TempDir(), "calls.log"), 1)
	if err := installNative(context.Background(), "agent", "v0.11.0"); err == nil {
		t.Fatal("expected an error when install.sh exits non-zero")
	}
}

func withEnvFileDir(t *testing.T, dir string) {
	t.Helper()
	orig := envFileDir
	envFileDir = dir
	t.Cleanup(func() { envFileDir = orig })
}

func TestReadEnvFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "update-detector")
	content := "LISTEN_ADDR=:8081\nAGGREGATOR_URL=http://agg:9090\nEMPTY=\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	values := readEnvFile(path)
	if values["LISTEN_ADDR"] != ":8081" || values["AGGREGATOR_URL"] != "http://agg:9090" {
		t.Fatalf("got %#v", values)
	}
}

func TestReadEnvFileMissing(t *testing.T) {
	values := readEnvFile(filepath.Join(t.TempDir(), "does-not-exist"))
	if len(values) != 0 {
		t.Fatalf("expected an empty map for a missing file, got %#v", values)
	}
}

func TestExistingConfigEnvAgentPassesThroughUnprefixed(t *testing.T) {
	dir := t.TempDir()
	withEnvFileDir(t, dir)
	content := "LISTEN_ADDR=:8081\nAGGREGATOR_URL=http://agg:9090\nAGENT_IDENTITY_FILE=/opt/customstate/agent-identity.json\n"
	if err := os.WriteFile(filepath.Join(dir, "update-detector"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	env := existingConfigEnv("agent")
	got := map[string]bool{}
	for _, e := range env {
		got[e] = true
	}
	if !got["LISTEN_ADDR=:8081"] {
		t.Fatalf("expected LISTEN_ADDR passed through unprefixed, got %v", env)
	}
	if !got["AGGREGATOR_URL=http://agg:9090"] {
		t.Fatalf("expected AGGREGATOR_URL passed through unprefixed, got %v", env)
	}
	if !got["STATE_DIR=/opt/customstate"] {
		t.Fatalf("expected STATE_DIR derived from AGENT_IDENTITY_FILE's directory, got %v", env)
	}
}

func TestExistingConfigEnvAggregatorTranslatesPrefixedNames(t *testing.T) {
	dir := t.TempDir()
	withEnvFileDir(t, dir)
	content := "LISTEN_ADDR=:9091\nTELEGRAM_BOT_TOKEN=tok123\nADMIN_APPLY_SHARED_SECRET=s3cret\nREGISTRY_FILE=/opt/aggdata/registry.json\n"
	if err := os.WriteFile(filepath.Join(dir, "update-aggregator"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	env := existingConfigEnv("aggregator")
	got := map[string]bool{}
	for _, e := range env {
		got[e] = true
	}
	if !got["AGGREGATOR_LISTEN_ADDR=:9091"] {
		t.Fatalf("expected LISTEN_ADDR translated to the prefixed AGGREGATOR_LISTEN_ADDR install.sh actually reads, got %v", env)
	}
	if !got["AGGREGATOR_TELEGRAM_BOT_TOKEN=tok123"] {
		t.Fatalf("expected TELEGRAM_BOT_TOKEN translated to AGGREGATOR_TELEGRAM_BOT_TOKEN, got %v", env)
	}
	// ADMIN_APPLY_SHARED_SECRET is deliberately NOT prefixed on the input
	// side (unlike the other two) -- this is the exact field that was
	// silently wiped before this fix existed.
	if !got["ADMIN_APPLY_SHARED_SECRET=s3cret"] {
		t.Fatalf("expected ADMIN_APPLY_SHARED_SECRET passed through unprefixed, got %v", env)
	}
	if !got["AGGREGATOR_DATA_DIR=/opt/aggdata"] {
		t.Fatalf("expected AGGREGATOR_DATA_DIR derived from REGISTRY_FILE's directory, got %v", env)
	}
}

func TestExistingConfigEnvCompanionIsNoop(t *testing.T) {
	if env := existingConfigEnv("companion"); env != nil {
		t.Fatalf("expected no config env for companion (it has no env file at all), got %v", env)
	}
}

func TestExistingConfigEnvMissingFileYieldsNoEnv(t *testing.T) {
	withEnvFileDir(t, t.TempDir()) // empty -- no env files at all
	if env := existingConfigEnv("agent"); env != nil {
		t.Fatalf("expected no env entries when there's no prior config file, got %v", env)
	}
	if env := existingConfigEnv("aggregator"); env != nil {
		t.Fatalf("expected no env entries when there's no prior config file, got %v", env)
	}
}

// TestInstallNativePreservesExistingSecret is the regression test for
// the bug this whole mechanism fixes: a self-update must not silently
// wipe an existing ADMIN_APPLY_SHARED_SECRET (or any other configured
// value) just because the re-invoked install.sh wasn't itself given it.
func TestInstallNativePreservesExistingSecret(t *testing.T) {
	dir := t.TempDir()
	withEnvFileDir(t, dir)
	content := "LISTEN_ADDR=:9090\nREGISTRY_FILE=/var/lib/update-aggregator/registry.json\nADMIN_APPLY_SHARED_SECRET=s3cret\n"
	if err := os.WriteFile(filepath.Join(dir, "update-aggregator"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	callLog := filepath.Join(t.TempDir(), "env.log")
	dirPath := t.TempDir()
	scriptPath := filepath.Join(dirPath, "install.sh")
	script := "#!/bin/sh\nenv | grep ADMIN_APPLY_SHARED_SECRET >> \"" + callLog + "\"\nexit 0\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	origPath := installShPath
	installShPath = scriptPath
	t.Cleanup(func() { installShPath = origPath })

	if err := installNative(context.Background(), "aggregator", "v2.0.0"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "ADMIN_APPLY_SHARED_SECRET=s3cret") {
		t.Fatalf("expected the existing secret to be passed through to install.sh's own environment, got: %q", got)
	}
}

func TestSelfUpdateCompanionAlwaysGoesNative(t *testing.T) {
	callLog := filepath.Join(t.TempDir(), "calls.log")
	writeFakeInstallSh(t, callLog, 0)
	// Deliberately no systemd unit dir / docker setup at all -- the
	// companion branch must never call Detect, so absence of both must
	// not matter.

	action := aggregator.Action{ID: "act1", Type: aggregator.ActionSelfUpdate, Component: "companion", TargetVersion: "v0.11.0"}
	result := SelfUpdate(context.Background(), action)
	if !result.Success {
		t.Fatalf("expected success, got: %#v", result)
	}
	got, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "companion v0.11.0\n" {
		t.Fatalf("got call log %q, want %q", got, "companion v0.11.0\n")
	}
}

func TestSelfUpdateAgentNativeDetected(t *testing.T) {
	dir := t.TempDir()
	withSystemdUnitDir(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "update-detector.service"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	callLog := filepath.Join(t.TempDir(), "calls.log")
	writeFakeInstallSh(t, callLog, 0)

	action := aggregator.Action{ID: "act1", Type: aggregator.ActionSelfUpdate, Component: "agent", TargetVersion: "v0.11.0"}
	result := SelfUpdate(context.Background(), action)
	if !result.Success {
		t.Fatalf("expected success, got: %#v", result)
	}
	got, _ := os.ReadFile(callLog)
	if string(got) != "agent v0.11.0\n" {
		t.Fatalf("got call log %q, want %q", got, "agent v0.11.0\n")
	}
}

func TestSelfUpdateAgentDockerDetected(t *testing.T) {
	dir := t.TempDir() // empty -- no native unit
	withSystemdUnitDir(t, dir)

	// A real directory -- updateDockerCompose sets cmd.Dir to whatever
	// working_dir reports, and os/exec requires that to actually exist.
	composeDir := t.TempDir()
	callLog := filepath.Join(t.TempDir(), "docker-calls.log")
	// Case patterns match the *exact* dotted label path Docker Compose
	// actually sets (confirmed against a real container:
	// com.docker.compose.project.config_files / .project.working_dir /
	// .service) -- a loose "*working_dir*" substring match would also
	// match the wrong "com.docker.compose.working_dir" (missing
	// ".project."), which is exactly the real bug this test caught live
	// against a real pi host: that wrong label name was queried, came
	// back empty, and updateDockerCompose failed with "missing expected
	// Docker Compose labels" despite the container having them all along.
	writeFakeDocker(t, `
case "$1" in
  ps)
    echo "cid123"
    ;;
  inspect)
    case "$3" in
      '{{.Config.Image}}')
        shift 3
        for id in "$@"; do
          case "$id" in
            cid123) echo "ghcr.io/sinwe/update-detector:v0.9.0" ;;
          esac
        done
        ;;
      *.project.config_files*) echo "`+composeDir+`/docker-compose.yml" ;;
      *.project.working_dir*) echo "`+composeDir+`" ;;
      *.service*) echo "update-detector" ;;
    esac
    ;;
  pull|tag|compose)
    echo "$@" >> "`+callLog+`"
    ;;
esac
`)

	action := aggregator.Action{ID: "act1", Type: aggregator.ActionSelfUpdate, Component: "agent", TargetVersion: "v0.11.0"}
	result := SelfUpdate(context.Background(), action)
	if !result.Success {
		t.Fatalf("expected success, got: %#v", result)
	}

	got, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatal(err)
	}
	calls := string(got)
	// Pull and tag target the repo directly, not through docker compose --
	// a plain `docker compose pull` would just re-fetch whatever tag the
	// compose file already references (":v0.9.0" here), not the actually
	// requested target version, which is exactly the bug this exercises.
	if !strings.Contains(calls, "pull ghcr.io/sinwe/update-detector:v0.11.0") {
		t.Fatalf("expected a pull of the target version, got log: %q", calls)
	}
	if !strings.Contains(calls, "tag ghcr.io/sinwe/update-detector:v0.11.0 ghcr.io/sinwe/update-detector:v0.9.0") {
		t.Fatalf("expected the target version retagged onto the currently-referenced tag, got log: %q", calls)
	}
	if !strings.Contains(calls, "-f "+composeDir+"/docker-compose.yml up -d update-detector") {
		t.Fatalf("expected an up -d call, got log: %q", calls)
	}
	if strings.Contains(calls, "compose pull") {
		t.Fatalf("must never run `docker compose pull` -- it would undo the local retag, got log: %q", calls)
	}
}

func TestSelfUpdateAgentNeitherDetected(t *testing.T) {
	withSystemdUnitDir(t, t.TempDir())
	t.Setenv("PATH", t.TempDir()) // no docker on PATH either

	action := aggregator.Action{ID: "act1", Type: aggregator.ActionSelfUpdate, Component: "agent", TargetVersion: "v0.11.0"}
	result := SelfUpdate(context.Background(), action)
	if result.Success {
		t.Fatalf("expected failure when neither native nor Docker is found, got: %#v", result)
	}
}

func TestSelfUpdateUnknownComponent(t *testing.T) {
	action := aggregator.Action{ID: "act1", Type: aggregator.ActionSelfUpdate, Component: "bogus", TargetVersion: "v0.11.0"}
	result := SelfUpdate(context.Background(), action)
	if result.Success {
		t.Fatal("expected failure for an unknown component")
	}
}

func TestSelfUpdateAmbiguousNotesBothPresent(t *testing.T) {
	dir := t.TempDir()
	withSystemdUnitDir(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "update-detector.service"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeFakeDocker(t, `
case "$1" in
  ps) echo "cid123" ;;
  inspect)
    shift 3
    for id in "$@"; do
      case "$id" in
        cid123) echo "ghcr.io/sinwe/update-detector:v0.9.0" ;;
      esac
    done
    ;;
esac
`)
	writeFakeInstallSh(t, filepath.Join(t.TempDir(), "calls.log"), 0)

	action := aggregator.Action{ID: "act1", Type: aggregator.ActionSelfUpdate, Component: "agent", TargetVersion: "v0.11.0"}
	result := SelfUpdate(context.Background(), action)
	if !result.Success {
		t.Fatalf("expected success (native takes precedence), got: %#v", result)
	}
	if !strings.Contains(result.Message, "both") {
		t.Fatalf("expected the ambiguous-state note in the message, got: %q", result.Message)
	}
}

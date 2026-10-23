//go:build darwin

package companion

import (
	"os"
	"path/filepath"
	"testing"
)

// TestExistingConfigEnvAgentReadsSidecar mirrors the linux
// TestExistingConfigEnvAgentPassesThroughUnprefixed, but for macOS's
// layout: the agent's config lives in the sidecar agent.env inside the
// state dir (STATE_DIR when set), not /etc/default/update-detector.
func TestExistingConfigEnvAgentReadsSidecar(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("STATE_DIR", dir)
	content := "LISTEN_ADDR=:8081\nAGGREGATOR_URL=http://agg:9090\nAGENT_IDENTITY_FILE=" + dir + "/agent-identity.json\n"
	if err := os.WriteFile(filepath.Join(dir, "agent.env"), []byte(content), 0o644); err != nil {
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
	if !got["STATE_DIR="+dir] {
		t.Fatalf("expected STATE_DIR derived from AGENT_IDENTITY_FILE's directory, got %v", env)
	}
}

func TestExistingConfigEnvAgentMissingSidecarYieldsNoEnv(t *testing.T) {
	t.Setenv("STATE_DIR", t.TempDir()) // empty -- no agent.env at all
	if env := existingConfigEnv("agent"); env != nil {
		t.Fatalf("expected no env entries when there's no sidecar file, got %v", env)
	}
}

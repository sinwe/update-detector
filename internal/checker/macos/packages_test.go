package macos

import (
	"os"
	"testing"
)

func TestParseBrewOutdated(t *testing.T) {
	raw, err := os.ReadFile("testdata/brew_outdated.json")
	if err != nil {
		t.Fatal(err)
	}
	result, err := parseBrewOutdated(raw)
	if err != nil {
		t.Fatalf("parseBrewOutdated: %v", err)
	}
	if result.Total != 3 {
		t.Fatalf("got total %d, want 3 (pinned entry skipped)", result.Total)
	}
	got := map[string][2]string{}
	for _, u := range result.Upgrades {
		got[u.Name] = [2]string{u.CurrentVersion, u.CandidateVersion}
		if u.Security {
			t.Errorf("%s: Security should always be false for brew (no severity signal)", u.Name)
		}
	}
	want := map[string][2]string{
		"deno":           {"2.9.6", "2.9.7"},
		"ffmpeg":         {"9.0.1_1", "9.0.2"},
		"github-copilot": {"1.1.14", "1.1.22"},
	}
	for name, versions := range want {
		if got[name] != versions {
			t.Errorf("%s: got current/candidate %v, want %v", name, got[name], versions)
		}
	}
}

func TestParseBrewOutdatedInvalid(t *testing.T) {
	if _, err := parseBrewOutdated([]byte("not json")); err == nil {
		t.Fatal("expected an error for invalid JSON, got nil")
	}
}

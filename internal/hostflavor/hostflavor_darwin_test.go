//go:build darwin

package hostflavor

import "testing"

func TestDetectDarwin(t *testing.T) {
	if got := Detect("/nonexistent/os-release"); got != "macos" {
		t.Fatalf("got %q, want macos (darwin always means macos, no os-release to sniff)", got)
	}
}

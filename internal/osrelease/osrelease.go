// Package osrelease parses /etc/os-release, shared by every checker
// implementation and by host-flavor detection.
package osrelease

import (
	"os"
	"strings"
)

// Parse parses the simple KEY=VALUE (optionally quoted) format used by
// /etc/os-release.
func Parse(raw string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.Index(line, "=")
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.Trim(strings.TrimSpace(line[idx+1:]), `"'`)
		out[key] = val
	}
	return out
}

// ReadID reads the ID field from the given os-release file (e.g. "ubuntu",
// "debian", "raspbian") — used to pick which checker implementation to run.
func ReadID(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return Parse(string(raw))["ID"], nil
}

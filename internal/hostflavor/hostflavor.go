// Package hostflavor picks which checker implementation to run based on the
// host's own OS, not the container's — the agent runs inside an
// Ubuntu-based image regardless of the host it's deployed on, so the
// container's own /etc/os-release is useless for this; the host's
// (host-mounted) os-release is what matters.
package hostflavor

import "update-detector/internal/osrelease"

// Detect reads the host's ID field from its (host-mounted) os-release file.
// Defaults to "ubuntu" if the file is missing/unreadable or the ID is
// unrecognized, preserving this project's original single-flavor behavior
// for anything Ubuntu-derived.
func Detect(osReleaseFile string) string {
	id, err := osrelease.ReadID(osReleaseFile)
	if err != nil || id == "" {
		return "ubuntu"
	}
	switch id {
	case "debian", "raspbian":
		return "debian"
	default:
		return "ubuntu"
	}
}

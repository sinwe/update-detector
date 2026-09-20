//go:build darwin

package hostflavor

// Detect unconditionally returns "macos" on this build -- macOS has no
// os-release file to sniff, and no other flavor could ever be correct
// here: only the macos checker package is linked into a darwin build's
// registry via cmd/update-detector/platforms_darwin.go (ubuntu/debian
// stay registered but are never selected), so any other name would just
// fail checker.New's registry lookup outright. Same posture as
// hostflavor_windows.go.
func Detect(osReleaseFile string) string {
	return "macos"
}

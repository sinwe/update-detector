//go:build windows

package hostflavor

// Detect unconditionally returns "windows" on this build -- there's no
// os-release file to sniff, and no other flavor could ever be correct
// here: only the windows checker package is linked into a windows build
// at all (see cmd/update-detector/platforms_windows.go), so any other
// name would just fail checker.New's registry lookup outright.
func Detect(osReleaseFile string) string {
	return "windows"
}

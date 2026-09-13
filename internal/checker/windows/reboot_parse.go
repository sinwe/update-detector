// Deliberately untagged, same reasoning as packages_parse.go: pure
// string matching with no OS-specific imports, testable on any platform,
// not just Windows.
package windows

import "strings"

// pendingRenameNoisePatterns lists (lowercased) path substrings for
// entries that reappear in PendingFileRenameOperations after nearly
// every single boot, forever, regardless of whether anything meaningful
// is actually waiting -- confirmed live on a real host: Windows Gaming
// Services' own proxy DLL and Microsoft Edge's background auto-updater
// both re-queue an entry here almost immediately after every reboot. A
// naive "list is non-empty" check is permanently true on any host with
// either installed (i.e. nearly all of them) and useless as a signal.
// Entries matching one of these are ignored; anything else still counts
// as a real pending change -- when in doubt, this errs toward reporting
// reboot-required, never toward hiding one.
var pendingRenameNoisePatterns = []string{
	`\gamingservicesproxy`,
	`\microsoft\edge\temp\`,
}

// isRoutinePendingRename reports whether entry (one raw string from
// PendingFileRenameOperations -- either half of a rename pair, or a
// delete pair's always-empty second half) matches a known-routine
// pattern. An empty string is routine by definition: it's never a real
// path on its own, only ever the "delete" half of a pair whose other
// half is what actually identifies what's pending.
func isRoutinePendingRename(entry string) bool {
	if entry == "" {
		return true
	}
	lower := strings.ToLower(entry)
	for _, pattern := range pendingRenameNoisePatterns {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	return false
}

// anyRealPendingRename reports whether entries (the raw string list from
// PendingFileRenameOperations) contains anything other than routine
// noise -- see isRoutinePendingRename.
func anyRealPendingRename(entries []string) bool {
	for _, v := range entries {
		if !isRoutinePendingRename(v) {
			return true
		}
	}
	return false
}

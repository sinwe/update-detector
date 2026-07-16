// Deliberately untagged, same reasoning as
// internal/checker/windows/packages_parse.go: pure string processing
// with no OS-specific imports, testable via fixture input on any
// platform, not just Windows.
package companion

import "regexp"

// kbPattern matches the "(KBnnnnnnn)" marker every Windows Update title
// carries (see internal/checker/windows/windowsupdate_parse.go, which
// builds PackageUpgrade.Name directly from that same Title) -- this is
// how splitPackageNames tells a Windows-Update-sourced request apart
// from a winget-sourced one, since both arrive at windowsApplier.Packages
// as the same kind of wire-level "package name" string (see execute.go's
// missingFromPending, which validates requested names against exactly
// what the checker itself reported as PackageUpgrade.Name).
var kbPattern = regexp.MustCompile(`\(KB(\d+)\)`)

// splitPackageNames splits names into Windows Update KB numbers (bare,
// no "KB" prefix -- what the Windows Update Agent API's own
// KBArticleIDs property uses) and everything else (assumed
// winget-sourced).
func splitPackageNames(names []string) (kbs, wingetNames []string) {
	for _, name := range names {
		if m := kbPattern.FindStringSubmatch(name); m != nil {
			kbs = append(kbs, m[1])
		} else {
			wingetNames = append(wingetNames, name)
		}
	}
	return kbs, wingetNames
}

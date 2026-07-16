// Deliberately untagged, same reasoning as
// internal/checker/windows/packages_parse.go: pure string processing
// with no OS-specific imports, testable via fixture input on any
// platform, not just Windows.
package companion

import "regexp"

// kbPattern matches each "(KBnnnnnnn)" marker
// internal/checker/windows/windowsupdate_parse.go's buildDisplayName
// always appends to PackageUpgrade.Name for a Windows-Update-sourced
// item -- one marker per KB the update actually carries, never a single
// comma-joined pair of parens, specifically so FindAllStringSubmatch
// below picks up every one of a multi-KB update's own KBs individually.
// This is how splitPackageNames tells a Windows-Update-sourced request
// apart from a winget-sourced one, since both arrive at
// windowsApplier.Packages as the same kind of wire-level "package name"
// string (see execute.go's missingFromPending, which validates requested
// names against exactly what the checker itself reported as
// PackageUpgrade.Name).
var kbPattern = regexp.MustCompile(`\(KB(\d+)\)`)

// splitPackageNames splits names into Windows Update KB numbers (bare,
// no "KB" prefix -- what the Windows Update Agent API's own
// KBArticleIDs property uses) and everything else (assumed
// winget-sourced). A name can itself carry more than one KB marker (a
// single update tied to multiple KBs) -- every one of them is extracted,
// not just the first.
func splitPackageNames(names []string) (kbs, wingetNames []string) {
	for _, name := range names {
		matches := kbPattern.FindAllStringSubmatch(name, -1)
		if len(matches) == 0 {
			wingetNames = append(wingetNames, name)
			continue
		}
		for _, m := range matches {
			kbs = append(kbs, m[1])
		}
	}
	return kbs, wingetNames
}

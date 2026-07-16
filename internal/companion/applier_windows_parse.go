// Deliberately untagged, same reasoning as
// internal/checker/windows/packages_parse.go: pure string processing
// with no OS-specific imports, testable via fixture input on any
// platform, not just Windows.
package companion

import "regexp"

// kbPattern matches each "(KBnnnnnnn)" marker
// internal/checker/windows/windowsupdate_parse.go's buildDisplayName
// always appends to PackageUpgrade.Name for a Windows-Update-sourced
// item that has at least one KB -- one marker per KB the update
// actually carries, never a single comma-joined pair of parens,
// specifically so FindAllStringSubmatch below picks up every one of a
// multi-KB update's own KBs individually.
var kbPattern = regexp.MustCompile(`\(KB(\d+)\)`)

// updateIDPattern matches the "{guid}" marker buildDisplayName appends
// instead, only for the update that has no KB at all (confirmed live:
// some driver updates) -- a different bracket shape than kbPattern's,
// deliberately, so a name can never be ambiguous between the two.
var updateIDPattern = regexp.MustCompile(`\{([0-9a-fA-F-]+)\}`)

// splitPackageNames splits names into Windows Update KB numbers (bare,
// no "KB" prefix -- what the Windows Update Agent API's own
// KBArticleIDs property uses), Windows Update UpdateIDs (for the update
// with no KB at all -- see updateIDPattern), and everything else
// (assumed winget-sourced, for now the only other package source this
// checker knows about -- see windowsApplier's own doc comment). A name
// can itself carry more than one KB marker (a single update tied to
// multiple KBs) -- every one of them is extracted, not just the first.
func splitPackageNames(names []string) (kbs, updateIDs, wingetNames []string) {
	for _, name := range names {
		if kbMatches := kbPattern.FindAllStringSubmatch(name, -1); len(kbMatches) > 0 {
			for _, m := range kbMatches {
				kbs = append(kbs, m[1])
			}
			continue
		}
		if idMatch := updateIDPattern.FindStringSubmatch(name); idMatch != nil {
			updateIDs = append(updateIDs, idMatch[1])
			continue
		}
		wingetNames = append(wingetNames, name)
	}
	return kbs, updateIDs, wingetNames
}

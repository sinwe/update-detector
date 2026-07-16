// Deliberately untagged, same reasoning as packages_parse.go: pure JSON
// parsing with no OS-specific imports, testable via fixture text on any
// platform, not just Windows.
package windows

import (
	"encoding/json"
	"fmt"
	"strings"

	"update-detector/internal/checker"
)

// windowsUpdateItem mirrors the JSON shape windowsupdate.go's PowerShell
// script emits per pending update.
type windowsUpdateItem struct {
	Title        string   `json:"Title"`
	KBArticleIDs []string `json:"KBArticleIDs"`
	IsMandatory  bool     `json:"IsMandatory"`
	// MsrcSeverity is "Critical"/"Important"/"Moderate"/"Low" for a
	// genuine MSRC-classified security update, or "" for anything else
	// (a feature/quality update with no security classification) -- the
	// real severity signal winget has no equivalent of at all (see
	// packages.go's own doc comment).
	MsrcSeverity string `json:"MsrcSeverity"`
}

// parseWindowsUpdateJSON parses windowsupdate.go's PowerShell script
// output. Treats "" and the literal "null" as zero updates rather than
// an error -- ConvertTo-Json can produce either for an empty collection
// depending on exactly how it's invoked, and "no updates pending" is a
// completely normal, common result, not a parse failure.
func parseWindowsUpdateJSON(raw []byte) (packageResult, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return packageResult{}, nil
	}

	var items []windowsUpdateItem
	if err := json.Unmarshal([]byte(trimmed), &items); err != nil {
		return packageResult{}, fmt.Errorf("windows update: parsing PowerShell output: %w", err)
	}

	var result packageResult
	for _, item := range items {
		security := item.MsrcSeverity != ""
		result.Total++
		if security {
			result.Security++
		}
		result.Upgrades = append(result.Upgrades, checker.PackageUpgrade{
			Name:             buildDisplayName(item.Title, item.KBArticleIDs),
			CandidateVersion: kbVersionString(item.KBArticleIDs),
			Security:         security,
		})
	}
	return result, nil
}

// kbVersionString renders KBArticleIDs (bare numbers, e.g. "5040442") as
// e.g. "KB5040442" (or "KB5040442, KB5040443" for the rare update tied
// to more than one), or "pending" if a genuine Windows Update entry
// somehow has none at all (seen in practice for some driver updates).
func kbVersionString(ids []string) string {
	if len(ids) == 0 {
		return "pending"
	}
	kbs := make([]string, len(ids))
	for i, id := range ids {
		kbs[i] = "KB" + id
	}
	return strings.Join(kbs, ", ")
}

// buildDisplayName appends every one of ids in its own "(KBnnnnnnn)"
// marker, always, regardless of whether title already happens to
// mention a KB number in its own text -- confirmed live, many real
// Windows Update titles never do at all (e.g. "Visual Studio Client
// Detector Utility", KB5001148, has no such text), and the companion's
// own apply side (internal/companion/applier_windows_parse.go's
// kbPattern) depends on this marker to recognize a requested package
// name as Windows-Update-sourced rather than winget-sourced -- without
// it, that name would silently misroute to winget instead and fail.
// Each KB gets its own parenthesized marker (not one comma-joined pair
// of parens) so a multi-KB update's every KB is still individually
// regex-matchable there.
func buildDisplayName(title string, ids []string) string {
	if len(ids) == 0 {
		return title
	}
	var b strings.Builder
	b.WriteString(title)
	for _, id := range ids {
		b.WriteString(" (KB")
		b.WriteString(id)
		b.WriteString(")")
	}
	return b.String()
}

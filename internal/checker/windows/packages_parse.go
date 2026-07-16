// Package windows implements checker.Checker for Windows hosts using
// winget for package detection and the registry for reboot-pending and
// OS version information (see windows.go, reboot.go -- both
// //go:build windows). This file is deliberately untagged: it's pure
// string processing with no OS-specific imports, which is what makes it
// unit-testable via fixture text on any platform, not just Windows --
// mirroring how internal/checker/debian's own dist-upgrade parsing is
// separated from its exec call.
package windows

import (
	"fmt"
	"sort"
	"strings"

	"update-detector/internal/checker"
)

// packageResult mirrors ubuntu/debian's own packageResult shape.
// Security is always 0: winget has no signal at all equivalent to apt's
// "-security" pocket, unlike Ubuntu/Debian.
type packageResult struct {
	Total    int
	Security int
	Upgrades []checker.PackageUpgrade
}

// wingetColumns are the column names `winget upgrade` prints in its
// header row, in the order this code looks for them. "Source" is
// legitimately sometimes absent (when every result comes from the same
// source, winget can omit the column entirely) -- every other column
// missing means the output format has changed in a way this parser
// doesn't understand.
var wingetColumns = []string{"Name", "Id", "Version", "Available", "Source"}

// parseWingetUpgrade parses `winget upgrade`'s table output, e.g.:
//
//	Name           Id                Version      Available    Source
//	------------------------------------------------------------------
//	Git             Git.Git          2.40.0       2.42.0       winget
//	3 upgrades available.
//
// Winget has no reliably-present JSON output across versions in the
// wild, so this scrapes the table the same "best-effort" way
// internal/checker/debian parses `apt-get -s dist-upgrade`'s text
// output -- locate the header row, then slice each data row at the
// header's own column-start offsets. Deliberately not a naive
// whitespace split: package names and Ids routinely contain spaces and
// dots, which would otherwise split mid-field.
//
// Winget's table format/column set has changed across App Installer
// versions, and winget itself may be entirely absent on locked-down or
// Server Windows machines -- returning an error here (rather than
// parsing garbage) lets the caller fall back to the previous cycle's
// value, the same posture every other subsystem in this codebase
// already uses for its own exec failures.
func parseWingetUpgrade(raw string) (packageResult, error) {
	lines := strings.Split(raw, "\n")

	headerIdx := -1
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "Name") && strings.Contains(line, "Id") && strings.Contains(line, "Version") {
			headerIdx = i
			break
		}
	}
	if headerIdx < 0 {
		return packageResult{}, fmt.Errorf("winget: could not find a recognizable header row (Name/Id/Version) -- output format may have changed")
	}

	cols, err := wingetColumnOffsets(lines[headerIdx])
	if err != nil {
		return packageResult{}, err
	}

	var result packageResult
	// The header is immediately followed by a "----" separator row, then
	// data rows, until a blank line or the trailing "N upgrades
	// available" summary.
	for _, line := range lines[headerIdx+2:] {
		if strings.TrimSpace(line) == "" {
			break
		}
		row := sliceColumns(line, cols)
		name := strings.TrimSpace(row["Name"])
		available := strings.TrimSpace(row["Available"])
		if name == "" || available == "" {
			continue
		}
		result.Total++
		result.Upgrades = append(result.Upgrades, checker.PackageUpgrade{
			Name:             name,
			CurrentVersion:   strings.TrimSpace(row["Version"]),
			CandidateVersion: available,
		})
	}
	return result, nil
}

// wingetColumnOffsets returns each expected column's start offset within
// header, keyed by column name -- derived from the header text itself
// rather than hardcoded positions, since winget's own column widths vary
// with content and locale.
func wingetColumnOffsets(header string) (map[string]int, error) {
	cols := map[string]int{}
	for _, name := range wingetColumns {
		idx := strings.Index(header, name)
		if idx < 0 {
			if name == "Source" {
				continue
			}
			return nil, fmt.Errorf("winget: header row missing expected column %q -- output format may have changed", name)
		}
		cols[name] = idx
	}
	return cols, nil
}

// sliceColumns cuts line into named substrings at each column's start
// offset (through the next column's start, or end of line for the
// last one). Winget aligns columns by fixed position, not delimiter, so
// this must slice by index rather than split on whitespace.
func sliceColumns(line string, cols map[string]int) map[string]string {
	type col struct {
		name  string
		start int
	}
	ordered := make([]col, 0, len(cols))
	for name, start := range cols {
		ordered = append(ordered, col{name, start})
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].start < ordered[j].start })

	row := map[string]string{}
	for i, c := range ordered {
		if c.start >= len(line) {
			row[c.name] = ""
			continue
		}
		end := len(line)
		if i+1 < len(ordered) {
			end = ordered[i+1].start
		}
		if end > len(line) {
			end = len(line)
		}
		row[c.name] = line[c.start:end]
	}
	return row
}

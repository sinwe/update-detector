//go:build !windows

package ubuntu

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"update-detector/internal/checker"
	"update-detector/internal/osrelease"
)

// These mirror the files do-release-upgrade itself reads to decide which
// upgrade channel to check: meta-release for every release, meta-release-lts
// for LTS-only tracking (the default), controlled by the release-upgrades
// file's Prompt= setting.
const (
	metaReleaseNormalURL = "https://changelogs.ubuntu.com/meta-release"
	metaReleaseLTSURL    = "https://changelogs.ubuntu.com/meta-release-lts"
)

type metaReleaseEntry struct {
	Dist      string
	Version   string // raw, e.g. "22.04 LTS"
	Supported bool
}

// checkOSRelease determines whether a newer supported Ubuntu release is
// available, for detection purposes only (no upgrade is ever run).
func checkOSRelease(ctx context.Context, client *http.Client, osReleaseFile, releaseUpgradesFile string) (checker.OSInfo, error) {
	osReleaseRaw, err := os.ReadFile(osReleaseFile)
	if err != nil {
		return checker.OSInfo{}, fmt.Errorf("os-release: reading %s: %w", osReleaseFile, err)
	}
	osRelease := osrelease.Parse(string(osReleaseRaw))
	versionID := osRelease["VERSION_ID"]

	info := checker.OSInfo{
		CurrentVersion:  versionID,
		CurrentCodename: osRelease["VERSION_CODENAME"],
	}
	if versionID == "" {
		return info, fmt.Errorf("os-release: %s has no VERSION_ID", osReleaseFile)
	}

	prompt := "lts"
	if raw, readErr := os.ReadFile(releaseUpgradesFile); readErr == nil {
		prompt = parsePrompt(string(raw))
	}

	url, skip := metaReleaseURL(prompt)
	if skip {
		return info, nil
	}

	body, err := fetchMetaRelease(ctx, client, url)
	if err != nil {
		return info, fmt.Errorf("meta-release: %w", err)
	}

	releases := parseMetaRelease(body)
	if latest, found := latestSupportedUpgrade(versionID, releases); found {
		info.UpdateAvailable = true
		info.LatestVersion = latest
	}
	return info, nil
}

func metaReleaseURL(prompt string) (url string, skip bool) {
	switch strings.ToLower(strings.TrimSpace(prompt)) {
	case "never":
		return "", true
	case "normal":
		return metaReleaseNormalURL, false
	default: // "lts" and anything unrecognized default to the conservative LTS-only channel
		return metaReleaseLTSURL, false
	}
}

func fetchMetaRelease(ctx context.Context, client *http.Client, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status %s from %s", resp.Status, url)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// parsePrompt extracts the Prompt= value from a release-upgrades file.
// Defaults to "lts" if not found, matching update-manager's own default.
func parsePrompt(raw string) string {
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "[") {
			continue
		}
		if rest, ok := cutPrefixFold(line, "prompt="); ok {
			return strings.ToLower(strings.TrimSpace(rest))
		}
	}
	return "lts"
}

func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) < len(prefix) || !strings.EqualFold(s[:len(prefix)], prefix) {
		return "", false
	}
	return s[len(prefix):], true
}

// parseMetaRelease parses the RFC822-style, blank-line-separated paragraphs
// used by changelogs.ubuntu.com/meta-release(-lts).
func parseMetaRelease(raw string) []metaReleaseEntry {
	var entries []metaReleaseEntry
	cur := map[string]string{}

	flush := func() {
		if dist := cur["Dist"]; dist != "" {
			entries = append(entries, metaReleaseEntry{
				Dist:      dist,
				Version:   cur["Version"],
				Supported: cur["Supported"] == "1",
			})
		}
		cur = map[string]string{}
	}

	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		// Continuation lines (RFC822 folding) start with whitespace; none of
		// the fields we care about span multiple lines, so skip them.
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			continue
		}
		idx := strings.Index(line, ":")
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		cur[key] = val
	}
	flush()
	return entries
}

// latestSupportedUpgrade returns the highest still-supported release version
// strictly greater than currentVersionID, if any.
func latestSupportedUpgrade(currentVersionID string, releases []metaReleaseEntry) (version string, found bool) {
	best := ""
	for _, r := range releases {
		if !r.Supported {
			continue
		}
		if !versionGreater(r.Version, currentVersionID) {
			continue
		}
		if best == "" || versionGreater(r.Version, best) {
			best = r.Version
		}
	}
	if best == "" {
		return "", false
	}
	return best, true
}

// versionGreater compares two "MAJOR.MINOR ..." version strings (e.g.
// "22.04 LTS" vs "24.04") numerically, since lexicographic comparison breaks
// once major versions reach two digits.
func versionGreater(a, b string) bool {
	aMaj, aMin, aOk := versionNumber(a)
	bMaj, bMin, bOk := versionNumber(b)
	if !aOk || !bOk {
		return false
	}
	if aMaj != bMaj {
		return aMaj > bMaj
	}
	return aMin > bMin
}

func versionNumber(raw string) (major, minor int, ok bool) {
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return 0, 0, false
	}
	parts := strings.SplitN(fields[0], ".", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	major, err1 := strconv.Atoi(parts[0])
	minor, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return major, minor, true
}

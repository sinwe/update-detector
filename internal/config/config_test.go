package config

import "testing"

// TestCheckerFieldsRoundTrips mitigates the real risk in CheckerFields: a
// copy-paste field-name mismatch silently breaking a config field (e.g.
// AptSourcesListD's value landing under the wrong key, or a key missing
// entirely) -- every field on Config that CheckerFields is documented to
// carry must show up, unchanged, under its expected key.
func TestCheckerFieldsRoundTrips(t *testing.T) {
	cfg := Config{
		Hostname:            "web01",
		AptSourcesList:      "/etc/apt/sources.list",
		AptSourcesListD:     "/etc/apt/sources.list.d",
		DpkgStatusFile:      "/var/lib/dpkg/status",
		AptListsCacheDir:    "/var/lib/update-detector/apt/lists",
		OSReleaseFile:       "/etc/os-release",
		ReleaseUpgradesFile: "/etc/update-manager/release-upgrades",
		RebootRequiredFile:  "/var/run/reboot-required",
	}

	fields := cfg.CheckerFields()

	want := map[string]string{
		"hostname":              cfg.Hostname,
		"apt_sources_list":      cfg.AptSourcesList,
		"apt_sources_list_d":    cfg.AptSourcesListD,
		"dpkg_status_file":      cfg.DpkgStatusFile,
		"apt_lists_cache_dir":   cfg.AptListsCacheDir,
		"os_release_file":       cfg.OSReleaseFile,
		"release_upgrades_file": cfg.ReleaseUpgradesFile,
		"reboot_required_file":  cfg.RebootRequiredFile,
	}
	for key, wantVal := range want {
		if got := fields[key]; got != wantVal {
			t.Errorf("field %q: got %q, want %q", key, got, wantVal)
		}
	}
	if len(fields) != len(want) {
		t.Errorf("got %d fields, want %d -- CheckerFields and this test's own expectations have drifted apart", len(fields), len(want))
	}
}

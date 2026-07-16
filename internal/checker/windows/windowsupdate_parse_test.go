package windows

import (
	"testing"

	"update-detector/internal/checker"
)

func TestParseWindowsUpdateJSONEmptyCollection(t *testing.T) {
	for _, raw := range []string{"[]", "null", "", "   \n"} {
		got, err := parseWindowsUpdateJSON([]byte(raw))
		if err != nil {
			t.Fatalf("parseWindowsUpdateJSON(%q): unexpected error: %v", raw, err)
		}
		if got.Total != 0 || len(got.Upgrades) != 0 {
			t.Fatalf("parseWindowsUpdateJSON(%q) = %#v, want zero value", raw, got)
		}
	}
}

func TestParseWindowsUpdateJSONSecurityAndFeatureUpdates(t *testing.T) {
	raw := `[
		{"Title":"2024-07 Cumulative Update for Windows 11 (KB5040442)","KBArticleIDs":["5040442"],"IsMandatory":true,"MsrcSeverity":"Critical"},
		{"Title":"2024-07 .NET Framework Update (KB5040498)","KBArticleIDs":["5040498"],"IsMandatory":false,"MsrcSeverity":""}
	]`

	got, err := parseWindowsUpdateJSON([]byte(raw))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Total != 2 {
		t.Fatalf("got Total %d, want 2", got.Total)
	}
	if got.Security != 1 {
		t.Fatalf("got Security %d, want 1 -- only the Critical-severity update should count", got.Security)
	}
	want := []checker.PackageUpgrade{
		{Name: "2024-07 Cumulative Update for Windows 11 (KB5040442) (KB5040442)", CandidateVersion: "KB5040442", Security: true},
		{Name: "2024-07 .NET Framework Update (KB5040498) (KB5040498)", CandidateVersion: "KB5040498", Security: false},
	}
	if len(got.Upgrades) != len(want) {
		t.Fatalf("got %d upgrades, want %d: %#v", len(got.Upgrades), len(want), got.Upgrades)
	}
	for i, w := range want {
		if got.Upgrades[i] != w {
			t.Fatalf("upgrade %d: got %#v, want %#v", i, got.Upgrades[i], w)
		}
	}
}

// TestParseWindowsUpdateJSONTitleWithoutOwnKBMention is the regression
// test for a real bug caught live: not every Windows Update title
// mentions its own KB number in the title text at all (confirmed live:
// "Visual Studio Client Detector Utility", KB5001148, has no such
// text) -- Name must still always carry an appended "(KBnnnnnnn)"
// marker regardless, since the companion's own apply side depends on
// it to recognize this as Windows-Update-sourced rather than
// winget-sourced (see internal/companion/applier_windows_parse.go).
// Confirmed live: without this, exactly this kind of item silently
// misrouted to winget at apply time and failed.
func TestParseWindowsUpdateJSONTitleWithoutOwnKBMention(t *testing.T) {
	raw := `[{"Title":"Visual Studio Client Detector Utility","KBArticleIDs":["5001148"],"IsMandatory":false,"MsrcSeverity":""}]`

	got, err := parseWindowsUpdateJSON([]byte(raw))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "Visual Studio Client Detector Utility (KB5001148)"
	if got.Upgrades[0].Name != want {
		t.Fatalf("got Name %q, want %q", got.Upgrades[0].Name, want)
	}
}

func TestParseWindowsUpdateJSONMultipleKBsAndMissingKB(t *testing.T) {
	raw := `[
		{"Title":"Multi-KB update","KBArticleIDs":["1111111","2222222"],"IsMandatory":false,"MsrcSeverity":"Moderate"},
		{"Title":"Driver update with no KB","KBArticleIDs":[],"IsMandatory":false,"MsrcSeverity":""}
	]`

	got, err := parseWindowsUpdateJSON([]byte(raw))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Upgrades[0].CandidateVersion != "KB1111111, KB2222222" {
		t.Fatalf("got CandidateVersion %q, want both KB numbers joined", got.Upgrades[0].CandidateVersion)
	}
	if got.Upgrades[0].Name != "Multi-KB update (KB1111111) (KB2222222)" {
		t.Fatalf("got Name %q, want each KB in its own marker", got.Upgrades[0].Name)
	}
	if got.Upgrades[1].CandidateVersion != "pending" {
		t.Fatalf("got CandidateVersion %q, want \"pending\" for no KB at all", got.Upgrades[1].CandidateVersion)
	}
	if got.Upgrades[1].Name != "Driver update with no KB" {
		t.Fatalf("got Name %q, want the title unchanged when there's no KB and no UpdateID either to append", got.Upgrades[1].Name)
	}
}

// TestParseWindowsUpdateJSONNoKBFallsBackToUpdateID is the regression
// test for a real bug caught live: some Windows Update items (driver
// updates, confirmed live: "Microsoft Corporation AudioProcessingObject
// Driver Update") have no KB at all, only a stable Identity.UpdateID --
// Name must fall back to a "{guid}" marker in that case so the item
// stays individually targetable for apply (see
// internal/companion/applier_windows_parse.go's updateIDPattern),
// instead of falling through unmarked and misrouting to winget.
func TestParseWindowsUpdateJSONNoKBFallsBackToUpdateID(t *testing.T) {
	raw := `[{"Title":"Microsoft Corporation AudioProcessingObject Driver Update (1.0.4.7057)","KBArticleIDs":[],"IsMandatory":false,"MsrcSeverity":"","UpdateID":"12345678-90ab-cdef-1234-567890abcdef"}]`

	got, err := parseWindowsUpdateJSON([]byte(raw))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "Microsoft Corporation AudioProcessingObject Driver Update (1.0.4.7057) {12345678-90ab-cdef-1234-567890abcdef}"
	if got.Upgrades[0].Name != want {
		t.Fatalf("got Name %q, want %q", got.Upgrades[0].Name, want)
	}
	if got.Upgrades[0].CandidateVersion != "pending" {
		t.Fatalf("got CandidateVersion %q, want \"pending\" -- CandidateVersion's own display text is unaffected by the UpdateID fallback", got.Upgrades[0].CandidateVersion)
	}
}

func TestParseWindowsUpdateJSONRejectsMalformed(t *testing.T) {
	if _, err := parseWindowsUpdateJSON([]byte("{not valid json")); err == nil {
		t.Fatal("expected an error for malformed JSON")
	}
}

func TestMergePackageResult(t *testing.T) {
	base := checker.PackageInfo{
		UpgradableTotal:    1,
		UpgradableSecurity: 1,
		Upgrades:           []checker.PackageUpgrade{{Name: "wu-item", Security: true}},
	}
	merged := mergePackageResult(base, packageResult{
		Total:    2,
		Security: 0,
		Upgrades: []checker.PackageUpgrade{{Name: "winget-item-1"}, {Name: "winget-item-2"}},
	})
	if merged.UpgradableTotal != 3 {
		t.Fatalf("got UpgradableTotal %d, want 3", merged.UpgradableTotal)
	}
	if merged.UpgradableSecurity != 1 {
		t.Fatalf("got UpgradableSecurity %d, want 1", merged.UpgradableSecurity)
	}
	if len(merged.Upgrades) != 3 {
		t.Fatalf("got %d upgrades, want 3: %#v", len(merged.Upgrades), merged.Upgrades)
	}
}

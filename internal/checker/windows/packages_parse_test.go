package windows

import (
	"strings"
	"testing"

	"update-detector/internal/checker"
)

func TestParseWingetUpgradeTypicalOutput(t *testing.T) {
	raw := "Name             Id                          Version      Available    Source\n" +
		"-----------------------------------------------------------------------------------\n" +
		"Git              Git.Git                     2.40.0       2.42.0       winget\n" +
		"Microsoft Edge   Microsoft.Edge              118.0.0.0    119.0.0.0    winget\n" +
		"\n" +
		"2 upgrades available.\n"

	got, err := parseWingetUpgrade(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Total != 2 {
		t.Fatalf("got Total %d, want 2", got.Total)
	}
	if got.Security != 0 {
		t.Fatalf("got Security %d, want 0 -- winget has no security signal at all", got.Security)
	}
	want := []checker.PackageUpgrade{
		{Name: "Git", CurrentVersion: "2.40.0", CandidateVersion: "2.42.0"},
		{Name: "Microsoft Edge", CurrentVersion: "118.0.0.0", CandidateVersion: "119.0.0.0"},
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

// TestParseWingetUpgradeNamesAndIdsWithSpacesAndDots is the regression
// test for why this can't be a naive whitespace split: "Microsoft Edge"
// (a Name with a space) and "Microsoft.Edge" (an Id with a dot) must
// each stay intact, sliced by the header's own column offsets rather
// than split on whitespace.
func TestParseWingetUpgradeNamesAndIdsWithSpacesAndDots(t *testing.T) {
	raw := "Name             Id                          Version      Available    Source\n" +
		"-----------------------------------------------------------------------------------\n" +
		"Microsoft Edge   Microsoft.Edge              118.0.0.0    119.0.0.0    winget\n"

	got, err := parseWingetUpgrade(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got.Upgrades) != 1 || got.Upgrades[0].Name != "Microsoft Edge" {
		t.Fatalf("got %#v, want Name %q intact", got.Upgrades, "Microsoft Edge")
	}
}

// TestParseWingetUpgradeNoSourceColumn covers winget's own documented
// behavior of omitting the Source column entirely when every result
// comes from the same source.
func TestParseWingetUpgradeNoSourceColumn(t *testing.T) {
	raw := "Name       Id             Version   Available\n" +
		"--------------------------------------------------\n" +
		"7-Zip      7zip.7zip      22.00     23.01\n"

	got, err := parseWingetUpgrade(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got.Upgrades) != 1 || got.Upgrades[0].Name != "7-Zip" || got.Upgrades[0].CandidateVersion != "23.01" {
		t.Fatalf("got %#v", got.Upgrades)
	}
}

func TestParseWingetUpgradeNoUpgradesAvailable(t *testing.T) {
	raw := "Name    Id    Version   Available   Source\n" +
		"-----------------------------------------------\n"

	got, err := parseWingetUpgrade(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Total != 0 || len(got.Upgrades) != 0 {
		t.Fatalf("got %#v, want zero upgrades", got)
	}
}

// TestParseWingetUpgradeMissingHeaderReturnsError is the regression test
// for winget's absence/format drift: no recognizable header at all
// (e.g. winget isn't installed, or its output changed) must be a
// descriptive error, not silently zero upgrades.
func TestParseWingetUpgradeMissingHeaderReturnsError(t *testing.T) {
	raw := "No installed package found matching input criteria.\n"

	_, err := parseWingetUpgrade(raw)
	if err == nil {
		t.Fatal("expected an error when no header row is found")
	}
}

// TestParseWingetUpgradeMissingExpectedColumnReturnsError covers a
// header that's present but missing a column this parser actually
// relies on (as opposed to the always-optional Source column) --
// output format drift severe enough that parsing further would risk
// silently misreading columns.
func TestParseWingetUpgradeMissingExpectedColumnReturnsError(t *testing.T) {
	raw := "Name    Id    Source\n" +
		"-----------------------\n"

	_, err := parseWingetUpgrade(raw)
	if err == nil {
		t.Fatal("expected an error when the header is missing an expected column (Version)")
	}
	if !strings.Contains(err.Error(), "Version") {
		t.Fatalf("expected the error to name the missing column, got: %v", err)
	}
}

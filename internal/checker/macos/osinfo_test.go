package macos

import "testing"

func TestParseSwVers(t *testing.T) {
	raw := "ProductName:\t\tmacOS\nProductVersion:\t\t27.0\nBuildVersion:\t\t26A428\n"
	name, version, err := parseSwVers(raw)
	if err != nil {
		t.Fatalf("parseSwVers: %v", err)
	}
	if name != "macOS" || version != "27.0" {
		t.Fatalf("got %q/%q, want macOS/27.0", name, version)
	}
}

func TestParseSwVersMissing(t *testing.T) {
	if _, _, err := parseSwVers("ProductName:\t\tmacOS\n"); err == nil {
		t.Fatal("expected an error when ProductVersion is missing, got nil")
	}
}

func TestParseSoftwareUpdateList(t *testing.T) {
	none := "Software Update Tool\n\nFinding available software\nNo new software available.\n"
	if parseSoftwareUpdateList(none) {
		t.Fatal("expected false for 'No new software available', got true")
	}
	some := "Software Update found the following new or updated software:\n* Label: macOS Tahoe 26.0.1-25A362\n\tTitle: macOS Tahoe 26.0.1, Version: 26.0.1\n"
	if !parseSoftwareUpdateList(some) {
		t.Fatal("expected true when a '* Label:' entry is offered, got false")
	}
}

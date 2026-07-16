package companion

import (
	"reflect"
	"testing"
)

func TestSplitPackageNames(t *testing.T) {
	cases := []struct {
		name            string
		input           []string
		wantKBs         []string
		wantUpdateIDs   []string
		wantWingetNames []string
	}{
		{
			name:    "single KB update",
			input:   []string{"2024-07 Cumulative Update for Windows 11 (KB5040442)"},
			wantKBs: []string{"5040442"},
		},
		{
			name:            "single winget package",
			input:           []string{"Git"},
			wantWingetNames: []string{"Git"},
		},
		{
			name: "mixed",
			input: []string{
				"2024-07 Cumulative Update for Windows 11 (KB5040442)",
				"Git",
				"2024-07 .NET Framework Update (KB5040498)",
				"Microsoft Edge",
			},
			wantKBs:         []string{"5040442", "5040498"},
			wantWingetNames: []string{"Git", "Microsoft Edge"},
		},
		{
			name: "empty input",
		},
		{
			// Regression test for a real bug caught live: a Windows
			// Update title that never mentions its own KB number in the
			// prose text at all (confirmed live: "Visual Studio Client
			// Detector Utility", KB5001148) -- windowsupdate_parse.go's
			// buildDisplayName always appends the marker regardless, so
			// this must still be recognized as a KB target here, not
			// misrouted to winget.
			name:    "KB update whose title never mentions its own KB",
			input:   []string{"Visual Studio Client Detector Utility (KB5001148)"},
			wantKBs: []string{"5001148"},
		},
		{
			// A single update tied to more than one KB gets one marker
			// per KB (see buildDisplayName), not one comma-joined pair
			// of parens -- every one of them must be extracted, not
			// just the first.
			name:    "update with multiple KB markers",
			input:   []string{"Multi-KB update (KB1111111) (KB2222222)"},
			wantKBs: []string{"1111111", "2222222"},
		},
		{
			// Regression test for a real bug caught live: some Windows
			// Update items (driver updates, confirmed live: "Microsoft
			// Corporation AudioProcessingObject Driver Update") have no
			// KB at all -- buildDisplayName falls back to a "{guid}"
			// marker (Identity.UpdateID) for these, which must route to
			// Windows Update targeting-by-ID, not winget.
			name:          "Windows Update item with no KB, only an UpdateID",
			input:         []string{"Microsoft Corporation AudioProcessingObject Driver Update (1.0.4.7057) {12345678-90ab-cdef-1234-567890abcdef}"},
			wantUpdateIDs: []string{"12345678-90ab-cdef-1234-567890abcdef"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotKBs, gotUpdateIDs, gotWingetNames := splitPackageNames(c.input)
			if !reflect.DeepEqual(gotKBs, c.wantKBs) {
				t.Errorf("kbs = %#v, want %#v", gotKBs, c.wantKBs)
			}
			if !reflect.DeepEqual(gotUpdateIDs, c.wantUpdateIDs) {
				t.Errorf("updateIDs = %#v, want %#v", gotUpdateIDs, c.wantUpdateIDs)
			}
			if !reflect.DeepEqual(gotWingetNames, c.wantWingetNames) {
				t.Errorf("wingetNames = %#v, want %#v", gotWingetNames, c.wantWingetNames)
			}
		})
	}
}

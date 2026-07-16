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
		wantWingetNames []string
	}{
		{
			name:            "single KB update",
			input:           []string{"2024-07 Cumulative Update for Windows 11 (KB5040442)"},
			wantKBs:         []string{"5040442"},
			wantWingetNames: nil,
		},
		{
			name:            "single winget package",
			input:           []string{"Git"},
			wantKBs:         nil,
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
			name:            "empty input",
			input:           nil,
			wantKBs:         nil,
			wantWingetNames: nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotKBs, gotWingetNames := splitPackageNames(c.input)
			if !reflect.DeepEqual(gotKBs, c.wantKBs) {
				t.Errorf("kbs = %#v, want %#v", gotKBs, c.wantKBs)
			}
			if !reflect.DeepEqual(gotWingetNames, c.wantWingetNames) {
				t.Errorf("wingetNames = %#v, want %#v", gotWingetNames, c.wantWingetNames)
			}
		})
	}
}

package ubuntu

import (
	"reflect"
	"testing"
)

func TestParseAptCheckCounts(t *testing.T) {
	tests := []struct {
		name         string
		raw          string
		wantTotal    int
		wantSecurity int
		wantErr      bool
	}{
		{name: "no updates", raw: "0;0\n", wantTotal: 0, wantSecurity: 0},
		{name: "mixed updates", raw: "5;2\n", wantTotal: 5, wantSecurity: 2},
		{name: "no trailing newline", raw: "12;0", wantTotal: 12, wantSecurity: 0},
		{name: "surrounding whitespace", raw: "  3 ; 1  \n", wantTotal: 3, wantSecurity: 1},
		{name: "missing separator", raw: "not-valid-output\n", wantErr: true},
		{name: "non-numeric", raw: "a;b\n", wantErr: true},
		{name: "empty", raw: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			total, security, err := parseAptCheckCounts(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got total=%d security=%d", total, security)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if total != tt.wantTotal || security != tt.wantSecurity {
				t.Fatalf("got total=%d security=%d, want total=%d security=%d", total, security, tt.wantTotal, tt.wantSecurity)
			}
		})
	}
}

func TestParsePackageNames(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []string
	}{
		{name: "empty", raw: "", want: nil},
		{name: "single", raw: "curl\n", want: []string{"curl"}},
		{name: "multiple with blank lines", raw: "curl\n\nopenssl\nlibc6\n", want: []string{"curl", "openssl", "libc6"}},
		{name: "extra fields ignored", raw: "curl/jammy-security 7.81.0 amd64 [upgradable from: 7.80.0]\n", want: []string{"curl/jammy-security"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parsePackageNames(tt.raw)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %#v, want %#v", got, tt.want)
			}
		})
	}
}

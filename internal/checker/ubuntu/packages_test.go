package ubuntu

import (
	"reflect"
	"testing"

	"update-detector/internal/checker"
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
		{
			name: "leading python warnings before the real counts line",
			raw: "/usr/lib/update-notifier/apt-check:351: Warning: W:Unable to read " +
				"/var/lib/ubuntu-advantage/apt-esm/etc/apt/apt.conf.d/ - DirectoryExists " +
				"(2: No such file or directory)\n  apt_pkg.init()\n5;2",
			wantTotal:    5,
			wantSecurity: 2,
		},
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

func TestParseUpgradableList(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []checker.PackageUpgrade
	}{
		{name: "empty", raw: "", want: nil},
		{
			name: "listing header ignored",
			raw:  "Listing...\ncurl/jammy-security 7.81.0-1ubuntu1.16 amd64 [upgradable from: 7.81.0-1ubuntu1.15]\n",
			want: []checker.PackageUpgrade{
				{Name: "curl", CurrentVersion: "7.81.0-1ubuntu1.15", CandidateVersion: "7.81.0-1ubuntu1.16", Security: true},
			},
		},
		{
			name: "multiple pockets in archive field",
			raw:  "libruby3.2/noble-updates,noble-security 3.2.3-1ubuntu0.24.04.8 amd64 [upgradable from: 3.2.3-1ubuntu0.24.04.7]\n",
			want: []checker.PackageUpgrade{
				{Name: "libruby3.2", CurrentVersion: "3.2.3-1ubuntu0.24.04.7", CandidateVersion: "3.2.3-1ubuntu0.24.04.8", Security: true},
			},
		},
		{
			name: "multiple packages with blank lines",
			raw:  "Listing...\ncurl/jammy 7.81.0 amd64 [upgradable from: 7.80.0]\n\nopenssl/jammy 3.0.2 amd64 [upgradable from: 3.0.1]\n",
			want: []checker.PackageUpgrade{
				{Name: "curl", CurrentVersion: "7.80.0", CandidateVersion: "7.81.0"},
				{Name: "openssl", CurrentVersion: "3.0.1", CandidateVersion: "3.0.2"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseUpgradableList(tt.raw)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %#v, want %#v", got, tt.want)
			}
		})
	}
}

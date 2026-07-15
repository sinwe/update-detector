//go:build !windows

package debian

import (
	"reflect"
	"testing"

	"update-detector/internal/checker"
)

// Based on real apt-get -s dist-upgrade output captured from a Raspberry Pi
// OS (Debian 13/trixie) host, with two synthetic lines added for coverage
// the real capture didn't happen to include: a dependency-only new install
// (no "[current]" bracket, must be excluded) and a Debian-Security-origin
// line (that host had no pending security updates at capture time).
const sampleDistUpgradeOutput = `Reading package lists...
Building dependency tree...
Reading state information...
Calculating upgrade...
Inst docker-compose-plugin [5.2.0-1~debian.13~trixie] (5.3.0-1~debian.13~trixie Docker CE:trixie [arm64])
Inst libdtovl0 [20260601-1] (20260626-1 Raspberry Pi Foundation:stable [arm64])
Inst libpisp1 [1.5.0-1] (1.6.0-1 Raspberry Pi Foundation:stable [arm64]) []
Inst raspinfo [20260601-1] (20260626-1 Raspberry Pi Foundation:stable [all])
Inst newdep (1.0-1 Raspberry Pi Foundation:stable [arm64])
Inst curl [7.88.0-1] (7.88.1-1 Debian-Security:trixie-security [arm64])
Conf docker-compose-plugin (5.3.0-1~debian.13~trixie Docker CE:trixie [arm64])
`

func TestParseDistUpgrade(t *testing.T) {
	got := parseDistUpgrade(sampleDistUpgradeOutput)

	if got.Total != 5 {
		t.Fatalf("got Total=%d, want 5 (newdep and the Conf line must be excluded): %#v", got.Total, got.Upgrades)
	}
	if got.Security != 1 {
		t.Fatalf("got Security=%d, want 1 (only the Debian-Security:trixie-security line)", got.Security)
	}

	want := []checker.PackageUpgrade{
		{Name: "docker-compose-plugin", CurrentVersion: "5.2.0-1~debian.13~trixie", CandidateVersion: "5.3.0-1~debian.13~trixie"},
		{Name: "libdtovl0", CurrentVersion: "20260601-1", CandidateVersion: "20260626-1"},
		{Name: "libpisp1", CurrentVersion: "1.5.0-1", CandidateVersion: "1.6.0-1"},
		{Name: "raspinfo", CurrentVersion: "20260601-1", CandidateVersion: "20260626-1"},
		{Name: "curl", CurrentVersion: "7.88.0-1", CandidateVersion: "7.88.1-1", Security: true},
	}
	if !reflect.DeepEqual(got.Upgrades, want) {
		t.Fatalf("got %#v, want %#v", got.Upgrades, want)
	}
}

func TestParseDistUpgradeEmpty(t *testing.T) {
	got := parseDistUpgrade("Reading package lists...\nBuilding dependency tree...\n0 upgraded, 0 newly installed\n")
	if got.Total != 0 || got.Security != 0 || got.Upgrades != nil {
		t.Fatalf("expected zero-value result, got %#v", got)
	}
}

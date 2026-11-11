//go:build !windows

package rhel

import (
	"reflect"
	"testing"

	"update-detector/internal/checker"
)

func TestStripArch(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"bash.x86_64", "bash"},
		{"kernel.aarch64", "kernel"},
		{"glibc.noarch", "glibc"},
		{"vim-minimal", "vim-minimal"},
		{"foo.bar", "foo.bar"}, // unknown arch suffix kept
		{"", ""},
	}
	for _, c := range cases {
		if got := stripArch(c.in); got != c.want {
			t.Errorf("stripArch(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseDnfSecuritySet(t *testing.T) {
	raw := `bash.x86_64
kernel.x86_64
glibc.i686
`
	got := parseDnfSecuritySet(raw)
	want := map[string]bool{"bash": true, "kernel": true, "glibc": true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
	if len(parseDnfSecuritySet("")) != 0 {
		t.Fatalf("empty input should yield empty set")
	}
}

func TestParseDnfUpgrades(t *testing.T) {
	sec := parseDnfSecuritySet("kernel.x86_64\nopenssl.x86_64\n")
	raw := `Last metadata expiration check: 0:12:34 ago on ...

bash.x86_64                    5.1.8-9.el9                    baseos
kernel.x86_64                  5.14.0-362.el9                 baseos
openssl.x86_64                 3.0.7-18.el9                   appstream
curl.x86_64                    7.76.1-23.el9                  baseos

`
	got, secCount := parseDnfUpgrades(raw, sec)
	want := []checker.PackageUpgrade{
		{Name: "bash", CandidateVersion: "5.1.8-9.el9", Security: false},
		{Name: "kernel", CandidateVersion: "5.14.0-362.el9", Security: true},
		{Name: "openssl", CandidateVersion: "3.0.7-18.el9", Security: true},
		{Name: "curl", CandidateVersion: "7.76.1-23.el9", Security: false},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
	if secCount != 2 {
		t.Fatalf("got securityCount %d, want 2", secCount)
	}

	// nil security set -> no security flags, no panic.
	got2, count2 := parseDnfUpgrades("bash.x86_64 5.1.8-9.el9 baseos\n", nil)
	if count2 != 0 || len(got2) != 1 || got2[0].Security {
		t.Fatalf("nil set handling wrong: %#v %d", got2, count2)
	}

	// empty input -> nil/empty, zero count
	got3, count3 := parseDnfUpgrades("", map[string]bool{})
	if len(got3) != 0 || count3 != 0 {
		t.Fatalf("empty input: got %#v %d", got3, count3)
	}
}
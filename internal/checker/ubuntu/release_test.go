//go:build !windows

package ubuntu

import "testing"
func TestParsePrompt(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "explicit lts", raw: "[DEFAULT]\nPrompt=lts\n", want: "lts"},
		{name: "explicit normal", raw: "[DEFAULT]\nPrompt=normal\n", want: "normal"},
		{name: "explicit never", raw: "[DEFAULT]\nPrompt=never\n", want: "never"},
		{name: "commented out defaults to lts", raw: "[DEFAULT]\n#Prompt=normal\n", want: "lts"},
		{name: "missing defaults to lts", raw: "[DEFAULT]\n", want: "lts"},
		{name: "case insensitive key", raw: "PROMPT=Normal\n", want: "normal"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parsePrompt(tt.raw); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMetaReleaseURL(t *testing.T) {
	if url, skip := metaReleaseURL("never"); !skip || url != "" {
		t.Fatalf("never: got url=%q skip=%v", url, skip)
	}
	if url, skip := metaReleaseURL("normal"); skip || url != metaReleaseNormalURL {
		t.Fatalf("normal: got url=%q skip=%v", url, skip)
	}
	if url, skip := metaReleaseURL("lts"); skip || url != metaReleaseLTSURL {
		t.Fatalf("lts: got url=%q skip=%v", url, skip)
	}
	if url, skip := metaReleaseURL(""); skip || url != metaReleaseLTSURL {
		t.Fatalf("empty (default): got url=%q skip=%v", url, skip)
	}
}

func TestParseMetaRelease(t *testing.T) {
	raw := `Dist: jammy
Name: Jammy Jellyfish
Version: 22.04 LTS
Supported: 1
Description: Ubuntu 22.04 LTS "Jammy Jellyfish"

Dist: noble
Name: Noble Numbat
Version: 24.04 LTS
Supported: 1
Description: Ubuntu 24.04 LTS "Noble Numbat"

Dist: mantic
Name: Mantic Minotaur
Version: 23.10
Supported: 0
`
	entries := parseMetaRelease(raw)
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3: %#v", len(entries), entries)
	}
	if entries[0].Dist != "jammy" || entries[0].Version != "22.04 LTS" || !entries[0].Supported {
		t.Fatalf("unexpected first entry: %#v", entries[0])
	}
	if entries[2].Dist != "mantic" || entries[2].Supported {
		t.Fatalf("unexpected third entry: %#v", entries[2])
	}
}

func TestVersionGreater(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"22.04 LTS", "20.04", true},
		{"20.04", "22.04 LTS", false},
		{"9.10", "10.04", false}, // regression check: lexicographic compare would get this backwards
		{"10.04", "9.10", true},
		{"22.04", "22.04", false},
		{"garbage", "22.04", false},
	}
	for _, tt := range tests {
		if got := versionGreater(tt.a, tt.b); got != tt.want {
			t.Fatalf("versionGreater(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestLatestSupportedUpgrade(t *testing.T) {
	releases := []metaReleaseEntry{
		{Dist: "focal", Version: "20.04 LTS", Supported: true},
		{Dist: "jammy", Version: "22.04 LTS", Supported: true},
		{Dist: "mantic", Version: "23.10", Supported: false},
		{Dist: "noble", Version: "24.04 LTS", Supported: true},
	}

	t.Run("upgrade available", func(t *testing.T) {
		got, found := latestSupportedUpgrade("20.04", releases)
		if !found || got != "24.04 LTS" {
			t.Fatalf("got %q found=%v, want 24.04 LTS/true", got, found)
		}
	})

	t.Run("already on latest", func(t *testing.T) {
		_, found := latestSupportedUpgrade("24.04", releases)
		if found {
			t.Fatalf("expected no upgrade available")
		}
	})

	t.Run("unsupported higher version ignored", func(t *testing.T) {
		got, found := latestSupportedUpgrade("22.10", releases)
		if !found || got != "24.04 LTS" {
			t.Fatalf("got %q found=%v, want 24.04 LTS/true (mantic 23.10 is unsupported and must be skipped)", got, found)
		}
	})
}

package version

import "testing"

func TestCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v1.0.0", "v1.0.0", 0},
		{"v0.9.0", "v0.10.0", -1}, // numeric, not lexicographic
		{"v0.10.0", "v0.9.0", 1},
		{"v0.10.0", "v0.10.1", -1},
		{"v1.0.0", "v0.99.99", 1},
		{"v0.10.0-rc1", "v0.10.0", -1}, // a pre-release orders before its release
		{"v0.10.0", "v0.10.0-rc1", 1},
		{"v0.10.0-rc1", "v0.10.0-rc2", -1},
		{"v0.10.0-rc2", "v0.10.0-rc1", 1},
		{"v0.10.0-rc1", "v0.10.0-rc1", 0},
		{"v0.9.0", "v0.10.0-rc1", -1}, // an older release's rc is still newer than a strictly older release
		{"0.10.0", "v0.10.0", 0},      // leading "v" is optional
	}
	for _, c := range cases {
		got, err := Compare(c.a, c.b)
		if err != nil {
			t.Fatalf("Compare(%q, %q) unexpected error: %v", c.a, c.b, err)
		}
		if got != c.want {
			t.Errorf("Compare(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestCompareRejectsUnparseable(t *testing.T) {
	cases := []string{"dev", "", "v1.0", "v1.0.0.0", "v1.x.0", "v0.10.0-beta1"}
	for _, v := range cases {
		if _, err := Compare(v, "v1.0.0"); err == nil {
			t.Errorf("Compare(%q, v1.0.0) expected an error, got nil", v)
		}
		if _, err := Compare("v1.0.0", v); err == nil {
			t.Errorf("Compare(v1.0.0, %q) expected an error, got nil", v)
		}
	}
}

func TestIsPreRelease(t *testing.T) {
	cases := map[string]bool{
		"v0.10.0":     false,
		"v0.10.0-rc1": true,
		"v0.10.0-rc2": true,
		"dev":         false, // unparseable -> false, not an error
	}
	for v, want := range cases {
		if got := IsPreRelease(v); got != want {
			t.Errorf("IsPreRelease(%q) = %v, want %v", v, got, want)
		}
	}
}

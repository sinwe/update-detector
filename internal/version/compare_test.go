package version

import "testing"

func TestCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		// Basic equality and numeric ordering
		{"v1.0.0", "v1.0.0", 0},
		{"v0.9.0", "v0.10.0", -1}, // numeric, not lexicographic
		{"v0.10.0", "v0.9.0", 1},
		{"v0.10.0", "v0.10.1", -1},
		{"v1.0.0", "v0.99.99", 1},

		// RC ordering (existing behavior preserved)
		{"v0.10.0-rc1", "v0.10.0", -1}, // a pre-release orders before its release
		{"v0.10.0", "v0.10.0-rc1", 1},
		{"v0.10.0-rc1", "v0.10.0-rc2", -1},
		{"v0.10.0-rc2", "v0.10.0-rc1", 1},
		{"v0.10.0-rc1", "v0.10.0-rc1", 0},
		{"v0.9.0", "v0.10.0-rc1", -1}, // an older release's rc is still newer than a strictly older release

		// 4-stage ordering: alpha < beta < rc < release
		{"v0.13.1-alpha1", "v0.13.1-beta1", -1},
		{"v0.13.1-beta1", "v0.13.1-rc1", -1},
		{"v0.13.1-rc1", "v0.13.1", -1},
		{"v0.13.1-alpha1", "v0.13.1", -1},
		{"v0.13.1-beta1", "v0.13.1", -1},

		// Reverse ordering
		{"v0.13.1", "v0.13.1-rc1", 1},
		{"v0.13.1-rc1", "v0.13.1-beta1", 1},
		{"v0.13.1-beta1", "v0.13.1-alpha1", 1},

		// Same stage, different numbers
		{"v0.13.1-alpha1", "v0.13.1-alpha2", -1},
		{"v0.13.1-alpha2", "v0.13.1-alpha1", 1},
		{"v0.13.1-alpha1", "v0.13.1-alpha1", 0},
		{"v0.13.1-beta1", "v0.13.1-beta2", -1},
		{"v0.13.1-rc1", "v0.13.1-rc2", -1},

		// Cross-stage: alpha2 < beta1 (stage dominates pre number)
		{"v0.13.1-alpha2", "v0.13.1-beta1", -1},
		{"v0.13.1-beta2", "v0.13.1-rc1", -1},
		{"v0.13.1-rc2", "v0.13.1", -1},

		// Different patch versions with stages
		{"v0.13.0", "v0.13.1-alpha1", -1},
		{"v0.13.1-alpha1", "v0.13.2", -1},

		// Leading "v" is optional
		{"0.10.0", "v0.10.0", 0},
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
	cases := []string{"dev", "", "v1.0", "v1.0.0.0", "v1.x.0", "v0.10.0-omega1", "v0.10.0-rc0", "v0.10.0-alpha0", "v0.10.0-beta0"}
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
		"v0.10.0":        false,
		"v0.10.0-rc1":    true,
		"v0.10.0-rc2":    true,
		"v0.13.1-alpha1": true,
		"v0.13.1-beta1":  true,
		"v0.13.1":        false,
		"dev":            false, // unparseable -> false, not an error
	}
	for v, want := range cases {
		if got := IsPreRelease(v); got != want {
			t.Errorf("IsPreRelease(%q) = %v, want %v", v, got, want)
		}
	}
}

func TestStage(t *testing.T) {
	cases := map[string]string{
		"v0.13.1-alpha1": "alpha",
		"v0.13.1-beta2":  "beta",
		"v0.13.1-rc3":    "rc",
		"v0.13.1":        "release",
	}
	for v, want := range cases {
		got, err := Stage(v)
		if err != nil {
			t.Errorf("Stage(%q) unexpected error: %v", v, err)
			continue
		}
		if got != want {
			t.Errorf("Stage(%q) = %q, want %q", v, got, want)
		}
	}

	// Unparseable versions should error
	if _, err := Stage("dev"); err == nil {
		t.Error("Stage(\"dev\") expected an error, got nil")
	}
}

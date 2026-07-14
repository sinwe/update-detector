package version

import (
	"fmt"
	"strconv"
	"strings"
)

// parsed is this repo's own tag shape: vMAJOR.MINOR.PATCH, optionally
// suffixed with -rcN. Not general semver -- there's no need to handle
// anything outside what .forgejo/workflows/release.yml actually tags.
type parsed struct {
	major, minor, patch int
	rc                  int // 0 means a real release (no -rc suffix)
}

func parseTag(v string) (parsed, error) {
	orig := v
	v = strings.TrimPrefix(v, "v")

	rc := 0
	if idx := strings.Index(v, "-rc"); idx != -1 {
		n, err := strconv.Atoi(v[idx+3:])
		if err != nil || n <= 0 {
			return parsed{}, fmt.Errorf("version: invalid -rc suffix in %q", orig)
		}
		rc = n
		v = v[:idx]
	}

	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return parsed{}, fmt.Errorf("version: %q is not of the form vMAJOR.MINOR.PATCH[-rcN]", orig)
	}
	nums := make([]int, 3)
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return parsed{}, fmt.Errorf("version: %q is not of the form vMAJOR.MINOR.PATCH[-rcN]", orig)
		}
		nums[i] = n
	}
	return parsed{major: nums[0], minor: nums[1], patch: nums[2], rc: rc}, nil
}

// Compare returns -1, 0, or 1 as a is less than, equal to, or greater
// than b, using this repo's own vMAJOR.MINOR.PATCH[-rcN] tag convention
// -- numeric, never lexicographic (v0.9.0 must sort below v0.10.0). A
// -rcN suffix always orders before the same MAJOR.MINOR.PATCH's real
// release; between two -rcN of the same version, the higher N is newer.
// Returns an error, rather than a guess, for anything that doesn't parse
// as this convention (e.g. the "dev" placeholder default) -- callers
// making a trust decision (see the aggregator's self-update
// dependency-ordering check) must fail closed on that, not silently
// treat an unparseable version as older or newer.
func Compare(a, b string) (int, error) {
	pa, err := parseTag(a)
	if err != nil {
		return 0, err
	}
	pb, err := parseTag(b)
	if err != nil {
		return 0, err
	}

	if c := cmpInt(pa.major, pb.major); c != 0 {
		return c, nil
	}
	if c := cmpInt(pa.minor, pb.minor); c != 0 {
		return c, nil
	}
	if c := cmpInt(pa.patch, pb.patch); c != 0 {
		return c, nil
	}
	return cmpInt(rcOrder(pa.rc), rcOrder(pb.rc)), nil
}

// IsPreRelease reports whether v has a -rcN suffix. Returns false (not
// an error) for anything unparseable -- this is only ever used as a
// filter over tags already known to come from this repo's own releases
// list, where a parse failure would be a bug in the caller, not
// something to propagate as "not a pre-release."
func IsPreRelease(v string) bool {
	p, err := parseTag(v)
	return err == nil && p.rc > 0
}

// rcOrder maps a -rcN suffix (0 meaning "no suffix, a real release") to
// a value where higher always means newer -- a real release must sort
// after every -rcN of the same MAJOR.MINOR.PATCH.
func rcOrder(rc int) int {
	if rc == 0 {
		return 1 << 30
	}
	return rc
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

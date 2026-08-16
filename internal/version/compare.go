package version

import (
	"fmt"
	"strconv"
	"strings"
)

// stage represents the pre-release stage within a version.
// Higher values are closer to release. A "release" (no suffix) is
// stageRelease, the highest.
type stage int

const (
	stageInvalid stage = iota
	stageAlpha        // -alphaN
	stageBeta         // -betaN
	stageRC           // -rcN
	stageRelease      // no suffix
)

// parsed is this repo's own tag shape: vMAJOR.MINOR.PATCH, optionally
// suffixed with -alphaN, -betaN, or -rcN. Not general semver -- there's
// no need to handle anything outside what .forgejo/workflows/release.yml
// actually tags.
type parsed struct {
	major, minor, patch int
	stage               stage
	pre                 int // pre-release number within the stage (1, 2, ...); 0 for release
}

func parseTag(v string) (parsed, error) {
	orig := v
	v = strings.TrimPrefix(v, "v")

	s := stageRelease
	pre := 0

	for _, suffix := range []struct {
		prefix string
		stg    stage
	}{
		{"-alpha", stageAlpha},
		{"-beta", stageBeta},
		{"-rc", stageRC},
	} {
		if idx := strings.Index(v, suffix.prefix); idx != -1 {
			n, err := strconv.Atoi(v[idx+len(suffix.prefix):])
			if err != nil || n <= 0 {
				return parsed{}, fmt.Errorf("version: invalid %s suffix in %q", suffix.prefix, orig)
			}
			s = suffix.stg
			pre = n
			v = v[:idx]
			break
		}
	}

	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return parsed{}, fmt.Errorf("version: %q is not of the form vMAJOR.MINOR.PATCH[-alphaN|-betaN|-rcN]", orig)
	}
	nums := make([]int, 3)
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return parsed{}, fmt.Errorf("version: %q is not of the form vMAJOR.MINOR.PATCH[-alphaN|-betaN|-rcN]", orig)
		}
		nums[i] = n
	}
	return parsed{major: nums[0], minor: nums[1], patch: nums[2], stage: s, pre: pre}, nil
}

// Compare returns -1, 0, or 1 as a is less than, equal to, or greater
// than b, using this repo's own vMAJOR.MINOR.PATCH[-alphaN|-betaN|-rcN]
// tag convention -- numeric, never lexicographic (v0.9.0 must sort below
// v0.10.0). Pre-release stages order alpha < beta < rc < release; within
// the same stage, higher N is newer. A pre-release always orders before
// the same MAJOR.MINOR.PATCH's real release.
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
	if c := cmpInt(int(pa.stage), int(pb.stage)); c != 0 {
		return c, nil
	}
	return cmpInt(pa.pre, pb.pre), nil
}

// IsPreRelease reports whether v has a -alphaN, -betaN, or -rcN suffix.
// Returns false (not an error) for anything unparseable -- this is only
// ever used as a filter over tags already known to come from this repo's
// own releases list, where a parse failure would be a bug in the caller,
// not something to propagate as "not a pre-release."
func IsPreRelease(v string) bool {
	p, err := parseTag(v)
	return err == nil && p.stage != stageRelease
}

// Stage returns the pre-release stage of v as a string ("alpha", "beta",
// "rc", or "release"), or an error if v doesn't parse.
func Stage(v string) (string, error) {
	p, err := parseTag(v)
	if err != nil {
		return "", err
	}
	return stageName(p.stage)
}

// Channels lists the valid channel names, ordered least to most stable --
// the same order a "minimum acceptable stage" selector should present
// them in.
var Channels = []string{"alpha", "beta", "rc", "release"}

func stageName(s stage) (string, error) {
	switch s {
	case stageAlpha:
		return "alpha", nil
	case stageBeta:
		return "beta", nil
	case stageRC:
		return "rc", nil
	case stageRelease:
		return "release", nil
	default:
		return "", fmt.Errorf("version: unknown stage %d", s)
	}
}

func parseChannel(channel string) (stage, error) {
	switch channel {
	case "alpha":
		return stageAlpha, nil
	case "beta":
		return stageBeta, nil
	case "rc":
		return stageRC, nil
	case "release":
		return stageRelease, nil
	default:
		return stageInvalid, fmt.Errorf("version: %q is not a valid channel (want one of %v)", channel, Channels)
	}
}

// ValidChannel reports whether channel is one of the four recognized
// channel names ("alpha", "beta", "rc", "release").
func ValidChannel(channel string) bool {
	_, err := parseChannel(channel)
	return err == nil
}

// MeetsChannel reports whether v's stage is at least as stable as channel
// (e.g. a "-rc1" tag meets the "beta" channel, since rc > beta; a
// "-beta1" tag does not meet the "rc" channel). channel must be one of
// Channels; v must parse as this repo's tag convention. Returns an error,
// rather than a guess, for either -- callers making a trust decision
// (which release is "available" on a given channel) must fail closed
// rather than silently admit or reject a tag.
func MeetsChannel(v, channel string) (bool, error) {
	p, err := parseTag(v)
	if err != nil {
		return false, err
	}
	minStage, err := parseChannel(channel)
	if err != nil {
		return false, err
	}
	return p.stage >= minStage, nil
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

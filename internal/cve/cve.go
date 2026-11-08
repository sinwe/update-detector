// Package cve defines the plugin contract for CVE/vulnerability detection.
//
// Mirrors internal/checker's registry pattern: each platform (or tool)
// registers a Detector under a name in its own init(), and callers select
// one by name without importing platform packages directly. A detector is
// optional per platform -- when none is registered or the underlying tool
// is missing, detection is skipped and reported as unavailable rather than
// as "clean", so a host with no detector never masquerades as patched.
package cve

import (
	"context"
	"fmt"

	"update-detector/internal/checker"
)

// Severity levels, ordered from most to least severe. Values are the
// canonical lowercase strings used in JSON output and by detectors.
const (
	SeverityCritical = "critical"
	SeverityHigh     = "high"
	SeverityMedium   = "medium"
	SeverityLow      = "low"
	SeverityUnknown  = "unknown"
)

// Fields is the same handoff shape as checker.Fields: a plain
// string-keyed map so callers can select a detector by name without
// importing platform packages.
type Fields map[string]string

// Detector scans a host for known CVEs affecting its OS/packages/runtime.
type Detector interface {
	Platform() string
	Detect(ctx context.Context, previous *checker.Status) (checker.VulnerabilityInfo, error)
}

// Factory constructs a Detector from Fields.
type Factory func(Fields) (Detector, error)

var registry = map[string]Factory{}

// Register adds name to the registry. Called from each detector's init().
func Register(name string, f Factory) {
	if _, exists := registry[name]; exists {
		panic(fmt.Sprintf("cve: %q already registered", name))
	}
	registry[name] = f
}

// New constructs the Detector registered under name. Returns an error for
// an unknown name, matching checker.New's posture.
func New(name string, fields Fields) (Detector, error) {
	f, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("cve: no detector registered for %q", name)
	}
	return f(fields)
}

// NormalizeSeverity maps free-form severity strings (as emitted by various
// tools) onto the canonical Severity* constants. Unknown input becomes
// SeverityUnknown rather than being dropped, so counts stay honest.
func NormalizeSeverity(raw string) string {
	switch raw {
	case "critical", "crit":
		return SeverityCritical
	case "high":
		return SeverityHigh
	case "medium", "moderate":
		return SeverityMedium
	case "low":
		return SeverityLow
	}
	return SeverityUnknown
}
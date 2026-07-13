// Package version holds the build-time version string, common to all
// three binaries (update-detector, update-aggregator,
// update-detector-companion). Set via
// -ldflags "-X update-detector/internal/version.Version=vX.Y.Z" in the
// release workflow; defaults to "dev" for local/unreleased builds.
package version

var Version = "dev"

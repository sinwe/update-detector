//go:build windows

package config

// Named pipe path for the companion token endpoint on Windows.
// Must match the path used by companionconfig's own defaultSocketPath.
const defaultCompanionSocketPath = `\\.\pipe\update-detector\companion-token`

//go:build windows

package companionconfig

// Named pipe path for the companion token endpoint on Windows.
// Must match the path used by the agent's companiontoken.Listen call
// in cmd/update-detector/platforms_windows.go.
const defaultSocketPath = `\\.\pipe\update-detector\companion-token`

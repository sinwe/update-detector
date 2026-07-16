//go:build !windows

package companiontoken

import (
	"fmt"
	"net"
	"os"
	"path/filepath"

	"update-detector/internal/aggregatorclient"
)

// Listen creates the socket's parent directory if needed, removes any stale
// socket file left over from a previous run, and binds a new Unix listener
// serving GET /companion/token, restricted to mode 0600.
func Listen(path string, identity aggregatorclient.Identity) (*Server, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("companiontoken: creating directory for %s: %w", path, err)
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("companiontoken: removing stale socket %s: %w", path, err)
	}

	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("companiontoken: listening on %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, fmt.Errorf("companiontoken: chmod %s: %w", path, err)
	}

	return newServer(ln, identity), nil
}

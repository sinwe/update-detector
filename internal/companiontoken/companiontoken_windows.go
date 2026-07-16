//go:build windows

package companiontoken

import (
	"fmt"

	"github.com/Microsoft/go-winio"
	"update-detector/internal/aggregatorclient"
)

// Listen creates a named pipe at path and returns a Server serving
// GET /companion/token over it. Named pipes on Windows are the closest
// equivalent to Unix domain sockets: they are kernel-local by
// construction and access-controlled via a Security Descriptor.
//
// The path should follow Windows named pipe convention:
//
//	\\.\pipe\update-detector\companion-token
//
// The pipe is created with the default DACL (owner full control, no
// other access) -- equivalent to Unix mode 0600 on the socket file.
func Listen(path string, identity aggregatorclient.Identity) (*Server, error) {
	// PipeConfig with default security: only the creating process's user
	// can connect -- equivalent to Unix mode 0600.
	ln, err := winio.ListenPipe(path, nil)
	if err != nil {
		return nil, fmt.Errorf("companiontoken: listening on named pipe %s: %w", path, err)
	}
	return newServer(ln, identity), nil
}

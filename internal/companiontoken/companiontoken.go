// Package companiontoken serves an agent's identity (agent_id + token) over
// a Unix domain socket, so a host-native companion process running on the
// same machine can fetch it without it ever touching the network or being
// persisted as a second on-disk copy.
//
// A Unix socket, not a TCP port with a RemoteAddr check, is deliberate: a
// host process hitting a container's *published* TCP port arrives at the
// container as the docker bridge gateway IP, not 127.0.0.1, unless that
// container happens to run with --network host -- an inconsistency observed
// in practice across this project's own two deployed hosts. A Unix socket
// sidesteps that entirely, since it's filesystem-local by construction: only
// something with access to the socket's path (and, since it's created
// mode 0600, the socket's owning UID or root) can ever connect.
package companiontoken

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"

	"update-detector/internal/aggregatorclient"
)

type Server struct {
	listener net.Listener
	httpSrv  *http.Server
}

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

	mux := http.NewServeMux()
	mux.HandleFunc("/companion/token", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(identity)
	})

	return &Server{
		listener: ln,
		httpSrv:  &http.Server{Handler: mux},
	}, nil
}

// Serve blocks, serving connections until Close is called.
func (s *Server) Serve() error {
	return s.httpSrv.Serve(s.listener)
}

func (s *Server) Close() error {
	return s.httpSrv.Close()
}

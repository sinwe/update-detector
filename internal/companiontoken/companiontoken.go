// Package companiontoken serves an agent's identity (agent_id + token) over
// a local transport (Unix domain socket on Linux/macOS, named pipe on
// Windows), so a host-native companion process running on the same machine
// can fetch it without it ever touching the network or being persisted as a
// second on-disk copy.
//
// A Unix socket / named pipe, not a TCP port with a RemoteAddr check, is
// deliberate: a host process hitting a container's *published* TCP port
// arrives at the container as the docker bridge gateway IP, not 127.0.0.1,
// unless that container happens to run with --network host -- an
// inconsistency observed in practice across this project's own two deployed
// hosts. A local socket sidesteps that entirely, since it's
// filesystem/kernel-local by construction: only something with access to
// the socket's path (and, since it's created mode 0600, the socket's owning
// UID or root) can ever connect.
package companiontoken

import (
	"encoding/json"
	"net"
	"net/http"

	"update-detector/internal/aggregatorclient"
)

// Server wraps the listener and HTTP server for the token endpoint.
// The Listen function (platform-specific) constructs it; Serve and Close
// are always shared.
type Server struct {
	listener net.Listener
	httpSrv  *http.Server
}

// newServer constructs a Server from an already-bound listener, wiring up
// the GET /companion/token handler. Called by each platform's Listen.
func newServer(ln net.Listener, identity aggregatorclient.Identity) *Server {
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
	}
}

// Serve blocks, serving connections until Close is called.
func (s *Server) Serve() error {
	return s.httpSrv.Serve(s.listener)
}

func (s *Server) Close() error {
	return s.httpSrv.Close()
}

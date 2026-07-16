// Package companionconfig loads update-detector-companion's runtime
// configuration from environment variables.
package companionconfig

import (
	"os"
	"strings"
)

type Config struct {
	// SocketPath is where the companion fetches this host's agent identity
	// (see internal/companiontoken). On Linux this is a Unix socket path;
	// on Windows it is a named pipe path (\\.\pipe\...). Must match
	// COMPANION_SOCKET_PATH / STATE_DIR on the agent side.
	SocketPath string

	// AggregatorURL is required -- there is no local-only mode for the
	// companion, unlike the agent.
	AggregatorURL string

	// AgentStatusURL is the local agent's own GET /status, used to
	// validate a requested action's packages are actually pending before
	// running anything.
	AgentStatusURL string
}

func Load() Config {
	return Config{
		SocketPath:     getEnv("COMPANION_SOCKET_PATH", defaultSocketPath),
		AggregatorURL:  strings.TrimSuffix(os.Getenv("AGGREGATOR_URL"), "/"),
		AgentStatusURL: getEnv("AGENT_STATUS_URL", "http://localhost:8080/status"),
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

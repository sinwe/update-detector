// Package aggregatorconfig loads the update-aggregator's runtime
// configuration from environment variables.
package aggregatorconfig

import "os"

type Config struct {
	ListenAddr   string
	RegistryFile string
}

func Load() (Config, error) {
	return Config{
		ListenAddr:   getEnv("LISTEN_ADDR", ":8080"),
		RegistryFile: getEnv("REGISTRY_FILE", "/var/lib/update-aggregator/registry.json"),
	}, nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

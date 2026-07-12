// Package aggregatorconfig loads the update-aggregator's runtime
// configuration from environment variables.
package aggregatorconfig

import "os"

type Config struct {
	ListenAddr   string
	RegistryFile string

	TelegramBotToken string
	TelegramChatID   string

	// AdminApplySharedSecret gates POST /admin/agents/{id}/apply. Empty
	// (the default) disables the endpoint entirely (501) -- triggering an
	// actual package upgrade is opt-in, unlike the rest of /admin which
	// trusts the network path alone. Intended to be injected by a
	// reverse-proxy (e.g. Authentik) after successful auth; the aggregator
	// checks it independently rather than trusting the proxy alone.
	AdminApplySharedSecret string
}

func Load() (Config, error) {
	return Config{
		ListenAddr:   getEnv("LISTEN_ADDR", ":8080"),
		RegistryFile: getEnv("REGISTRY_FILE", "/var/lib/update-aggregator/registry.json"),

		TelegramBotToken: os.Getenv("TELEGRAM_BOT_TOKEN"),
		TelegramChatID:   os.Getenv("TELEGRAM_CHAT_ID"),

		AdminApplySharedSecret: os.Getenv("ADMIN_APPLY_SHARED_SECRET"),
	}, nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

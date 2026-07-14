// Package aggregatorconfig loads the update-aggregator's runtime
// configuration from environment variables.
package aggregatorconfig

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

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

	// SelfUpdateCheckInterval controls how often the aggregator checks
	// Forgejo for a newer update-detector release (see
	// internal/selfupdate). Losing this on a restart is fine -- it's
	// purely an in-memory cache, re-fetched fresh either way.
	SelfUpdateCheckInterval time.Duration

	// SelfUpdateIncludePreRelease selects the channel self-update version
	// checks consider: false (the default) only ever surfaces a real
	// release; true also considers -rcN tags, and treats a newer
	// pre-release as available even over an older real release. See
	// internal/selfupdate.New.
	SelfUpdateIncludePreRelease bool
}

func Load() (Config, error) {
	selfUpdateCheckInterval, err := parseDuration("SELF_UPDATE_CHECK_INTERVAL", "24h")
	if err != nil {
		return Config{}, err
	}
	selfUpdateIncludePreRelease, err := parseBool("SELF_UPDATE_INCLUDE_PRERELEASE", false)
	if err != nil {
		return Config{}, err
	}

	return Config{
		ListenAddr:   getEnv("LISTEN_ADDR", ":8080"),
		RegistryFile: getEnv("REGISTRY_FILE", "/var/lib/update-aggregator/registry.json"),

		TelegramBotToken: os.Getenv("TELEGRAM_BOT_TOKEN"),
		TelegramChatID:   os.Getenv("TELEGRAM_CHAT_ID"),

		AdminApplySharedSecret: os.Getenv("ADMIN_APPLY_SHARED_SECRET"),

		SelfUpdateCheckInterval:     selfUpdateCheckInterval,
		SelfUpdateIncludePreRelease: selfUpdateIncludePreRelease,
	}, nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func parseDuration(key, fallback string) (time.Duration, error) {
	raw := getEnv(key, fallback)
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid %s %q: %w", key, raw, err)
	}
	return d, nil
}

func parseBool(key string, fallback bool) (bool, error) {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback, nil
	}
	b, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("invalid %s %q: %w", key, raw, err)
	}
	return b, nil
}

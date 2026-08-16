// Package aggregatorconfig loads the update-aggregator's runtime
// configuration from environment variables.
package aggregatorconfig

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"update-detector/internal/version"
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
	// GitHub for a newer update-detector release (see
	// internal/selfupdate). Losing this on a restart is fine -- it's
	// purely an in-memory cache, re-fetched fresh either way.
	SelfUpdateCheckInterval time.Duration

	// SelfUpdateChannel selects the minimum stage self-update version
	// checks consider (one of version.Channels: "alpha", "beta", "rc",
	// "release"): "release" (the default) only ever surfaces a real
	// release; a pre-release channel also considers tags at that stage or
	// more stable, and treats a newer one as available even over an older
	// real release. See internal/selfupdate.New.
	SelfUpdateChannel string
}

func Load() (Config, error) {
	selfUpdateCheckInterval, err := parseDuration("SELF_UPDATE_CHECK_INTERVAL", "24h")
	if err != nil {
		return Config{}, err
	}
	selfUpdateChannel, err := loadSelfUpdateChannel()
	if err != nil {
		return Config{}, err
	}

	return Config{
		ListenAddr:   getEnv("LISTEN_ADDR", ":8080"),
		RegistryFile: getEnv("REGISTRY_FILE", "/var/lib/update-aggregator/registry.json"),

		TelegramBotToken: os.Getenv("TELEGRAM_BOT_TOKEN"),
		TelegramChatID:   os.Getenv("TELEGRAM_CHAT_ID"),

		AdminApplySharedSecret: os.Getenv("ADMIN_APPLY_SHARED_SECRET"),

		SelfUpdateCheckInterval: selfUpdateCheckInterval,
		SelfUpdateChannel:       selfUpdateChannel,
	}, nil
}

// loadSelfUpdateChannel reads SELF_UPDATE_CHANNEL (one of version.Channels),
// defaulting to "release". Falls back to the older, now-removed
// SELF_UPDATE_INCLUDE_PRERELEASE boolean when SELF_UPDATE_CHANNEL isn't
// set, so an existing deployment's env file keeps working unchanged after
// this upgrade: true mapped to "alpha" (its old behavior -- admit any
// pre-release stage), false to "release".
func loadSelfUpdateChannel() (string, error) {
	if raw := os.Getenv("SELF_UPDATE_CHANNEL"); raw != "" {
		if !version.ValidChannel(raw) {
			return "", fmt.Errorf("invalid SELF_UPDATE_CHANNEL %q (want one of %v)", raw, version.Channels)
		}
		return raw, nil
	}
	legacyIncludePreRelease, err := parseBool("SELF_UPDATE_INCLUDE_PRERELEASE", false)
	if err != nil {
		return "", err
	}
	if legacyIncludePreRelease {
		return "alpha", nil
	}
	return "release", nil
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

// Package config loads the update-detector's runtime configuration from
// environment variables, with defaults matching the mount layout documented
// in docker-compose.yml.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	ListenAddr string
	Hostname   string

	CheckInterval time.Duration

	AptSourcesList   string
	AptSourcesListD  string
	DpkgStatusFile   string
	AptListsCacheDir string

	OSReleaseFile       string
	ReleaseUpgradesFile string
	RebootRequiredFile  string

	StateFile string

	TelegramBotToken string
	TelegramChatID   string

	NotifyOnStartup bool

	// AggregatorURL enables push mode when set: this agent will enroll with
	// and report to a central update-aggregator (see internal/aggregator),
	// in addition to (never instead of) serving /status and /healthz
	// locally and notifying Telegram directly.
	AggregatorURL     string
	AgentIdentityFile string
}

func Load() (Config, error) {
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "unknown"
	}

	interval, err := parseDuration("CHECK_INTERVAL", "6h")
	if err != nil {
		return Config{}, err
	}

	notifyOnStartup, err := parseBool("NOTIFY_ON_STARTUP", false)
	if err != nil {
		return Config{}, err
	}

	cfg := Config{
		ListenAddr: getEnv("LISTEN_ADDR", ":8080"),
		Hostname:   getEnv("HOSTNAME_OVERRIDE", hostname),

		CheckInterval: interval,

		AptSourcesList:   getEnv("APT_SOURCES_LIST", "/host/etc/apt/sources.list"),
		AptSourcesListD:  getEnv("APT_SOURCES_LIST_D", "/host/etc/apt/sources.list.d"),
		DpkgStatusFile:   getEnv("DPKG_STATUS_FILE", "/host/var/lib/dpkg/status"),
		AptListsCacheDir: getEnv("APT_LISTS_CACHE_DIR", "/var/lib/update-detector/apt/lists"),

		OSReleaseFile:       getEnv("OS_RELEASE_FILE", "/host/etc/os-release"),
		ReleaseUpgradesFile: getEnv("RELEASE_UPGRADES_FILE", "/host/etc/update-manager/release-upgrades"),
		RebootRequiredFile:  getEnv("REBOOT_REQUIRED_FILE", "/host/var/run/reboot-required"),

		StateFile: getEnv("STATE_FILE", "/var/lib/update-detector/state.json"),

		TelegramBotToken: os.Getenv("TELEGRAM_BOT_TOKEN"),
		TelegramChatID:   os.Getenv("TELEGRAM_CHAT_ID"),

		NotifyOnStartup: notifyOnStartup,

		AggregatorURL:     strings.TrimSuffix(os.Getenv("AGGREGATOR_URL"), "/"),
		AgentIdentityFile: getEnv("AGENT_IDENTITY_FILE", "/var/lib/update-detector/agent-identity.json"),
	}

	return cfg, nil
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

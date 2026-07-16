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

	"update-detector/internal/checker"
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

	// CompanionSocketPath is where GET /companion/token is served -- a Unix
	// socket (Linux) or named pipe (Windows) rather than the TCP mux, so a
	// host-native companion process can fetch this agent's identity without
	// it ever touching the network or a second on-disk copy.
	// See internal/companiontoken.
	CompanionSocketPath string
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

		CompanionSocketPath: getEnv("COMPANION_SOCKET_PATH", defaultCompanionSocketPath),
	}

	return cfg, nil
}

// CheckerFields translates this flat Config into the string-keyed bag a
// registered checker.Factory actually consumes (see checker.Fields) --
// Config itself stays flat rather than growing a nested per-platform
// section; this is a 3-4 platform project, not a plugin ecosystem, so
// that would be over-engineering. Every key here is always populated
// regardless of which platform is actually selected -- an unused key
// (e.g. "release_upgrades_file" for a factory whose Config has no such
// field) is simply ignored by that factory, not an error.
func (c Config) CheckerFields() checker.Fields {
	return checker.Fields{
		"hostname":              c.Hostname,
		"apt_sources_list":      c.AptSourcesList,
		"apt_sources_list_d":    c.AptSourcesListD,
		"dpkg_status_file":      c.DpkgStatusFile,
		"apt_lists_cache_dir":   c.AptListsCacheDir,
		"os_release_file":       c.OSReleaseFile,
		"release_upgrades_file": c.ReleaseUpgradesFile,
		"reboot_required_file":  c.RebootRequiredFile,
	}
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

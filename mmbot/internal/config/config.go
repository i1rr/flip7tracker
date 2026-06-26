// Package config loads and validates the flip7bot (mmbot) process configuration
// from the environment (optionally seeded from a .env file via godotenv). It
// fails fast, listing every missing required variable, and asserts the HMAC
// signing key is at least 32 bytes (256-bit). Startup logging must go through
// Redacted() so no secret value is ever written to the log.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/joho/godotenv"
)

// MinHMACKeyBytes is the minimum acceptable length of HMAC_KEY. The key signs
// all inbound nav-state; a 256-bit (32-byte) key is the floor. Generate one
// with: openssl rand -hex 32  (yields a 64-char string, comfortably above 32).
const MinHMACKeyBytes = 32

// Defaults for optional variables.
const (
	// DefaultDatabaseURL points at the flip7_data volume mount inside the
	// container.
	DefaultDatabaseURL = "/data/flip7.db"
	// DefaultListenAddr binds 0.0.0.0 inside the container; the port is
	// published only to the Mattermost bridge, never to the host.
	DefaultListenAddr = "0.0.0.0:8068"
	// DefaultLogLevel is used when LOG_LEVEL is unset or unrecognised.
	DefaultLogLevel = "info"
)

// Config is the fully-resolved, validated process configuration.
type Config struct {
	// Mattermost connection / addressing.
	MMURL      string // MM_URL, e.g. http://172.28.0.1:8065
	MMBotToken string // MM_BOT_TOKEN (secret)
	MMTeam     string // MM_TEAM (team name/slug)
	MMChannel  string // MM_CHANNEL (channel name/slug)

	// Owner identity. At least one of OwnerUserID / OwnerUsername must be set;
	// internal/mm resolves a username to an id at startup when the id is absent.
	OwnerUsername string // OWNER_USERNAME
	OwnerUserID   string // OWNER_USER_ID

	// Secrets.
	HMACKey         string // HMAC_KEY (server-only signing key, >= 32 bytes)
	SlashTokenFlip7 string // SLASH_TOKEN_FLIP7

	// Storage.
	DatabaseURL string // DATABASE_URL, defaults to /data/flip7.db

	// Inbound listener / integration URLs.
	ListenAddr         string // LISTEN_ADDR, defaults to 0.0.0.0:8068 (bridge-only)
	IntegrationBaseURL string // INTEGRATION_BASE_URL, base for action/dialog/slash URLs

	// Logging.
	LogLevel string // LOG_LEVEL, defaults to "info"
}

// Load reads the optional .env file at envPath (ignored if it does not exist;
// missing is non-fatal because the environment may be set by docker compose),
// then resolves the configuration from the process environment. Pass "" to skip
// loading a file and read the environment directly.
func Load(envPath string) (*Config, error) {
	if envPath != "" {
		if _, err := os.Stat(envPath); err == nil {
			if err := godotenv.Load(envPath); err != nil {
				return nil, fmt.Errorf("loading %s: %w", envPath, err)
			}
		}
	}
	return FromEnv()
}

// FromEnv resolves the configuration from os.Getenv only (no file loading). It
// is the unit-testable core of Load.
func FromEnv() (*Config, error) {
	c := &Config{
		MMURL:              strings.TrimSpace(os.Getenv("MM_URL")),
		MMBotToken:         os.Getenv("MM_BOT_TOKEN"),
		MMTeam:             strings.TrimSpace(os.Getenv("MM_TEAM")),
		MMChannel:          strings.TrimSpace(os.Getenv("MM_CHANNEL")),
		OwnerUsername:      strings.TrimSpace(os.Getenv("OWNER_USERNAME")),
		OwnerUserID:        strings.TrimSpace(os.Getenv("OWNER_USER_ID")),
		HMACKey:            os.Getenv("HMAC_KEY"),
		SlashTokenFlip7:    os.Getenv("SLASH_TOKEN_FLIP7"),
		DatabaseURL:        getDefault("DATABASE_URL", DefaultDatabaseURL),
		ListenAddr:         getDefault("LISTEN_ADDR", DefaultListenAddr),
		IntegrationBaseURL: strings.TrimSpace(os.Getenv("INTEGRATION_BASE_URL")),
		LogLevel:           getDefault("LOG_LEVEL", DefaultLogLevel),
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// getDefault returns the trimmed value of the named env var, or def when unset
// or blank.
func getDefault(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// validate fails fast, accumulating every problem so a single run surfaces all
// misconfiguration rather than one var at a time.
func (c *Config) validate() error {
	var missing []string
	req := func(name, val string) {
		if strings.TrimSpace(val) == "" {
			missing = append(missing, name)
		}
	}
	req("MM_URL", c.MMURL)
	req("MM_BOT_TOKEN", c.MMBotToken)
	req("MM_TEAM", c.MMTeam)
	req("MM_CHANNEL", c.MMChannel)
	req("HMAC_KEY", c.HMACKey)
	req("SLASH_TOKEN_FLIP7", c.SlashTokenFlip7)
	req("INTEGRATION_BASE_URL", c.IntegrationBaseURL)

	var problems []string
	if len(missing) > 0 {
		problems = append(problems, "missing required env var(s): "+strings.Join(missing, ", "))
	}
	// Owner identity: require at least one of id/username.
	if c.OwnerUserID == "" && c.OwnerUsername == "" {
		problems = append(problems, "at least one of OWNER_USER_ID or OWNER_USERNAME must be set")
	}
	// HMAC key strength: only meaningful when present.
	if c.HMACKey != "" && len([]byte(c.HMACKey)) < MinHMACKeyBytes {
		problems = append(problems, fmt.Sprintf(
			"HMAC_KEY must be at least %d bytes (256-bit); got %d. Generate one with: openssl rand -hex 32",
			MinHMACKeyBytes, len([]byte(c.HMACKey))))
	}

	if len(problems) > 0 {
		return fmt.Errorf("invalid configuration: %s", strings.Join(problems, "; "))
	}
	return nil
}

// SlogLevel parses LogLevel into a slog.Level, defaulting to Info for any
// unrecognised value.
func (c *Config) SlogLevel() slog.Level {
	switch strings.ToLower(strings.TrimSpace(c.LogLevel)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// Redacted returns a map of the non-secret configuration suitable for logging
// at startup. Secrets (bot token, HMAC key, slash token) are deliberately
// omitted; only a boolean presence indicator is exposed for each.
func (c *Config) Redacted() map[string]any {
	return map[string]any{
		"mm_url":               c.MMURL,
		"mm_team":              c.MMTeam,
		"mm_channel":           c.MMChannel,
		"owner_user_id":        c.OwnerUserID,
		"owner_username":       c.OwnerUsername,
		"database_url":         c.DatabaseURL,
		"listen_addr":          c.ListenAddr,
		"integration_base_url": c.IntegrationBaseURL,
		"log_level":            c.LogLevel,
		"mm_bot_token_set":     c.MMBotToken != "",
		"hmac_key_set":         c.HMACKey != "",
		"slash_token_set":      c.SlashTokenFlip7 != "",
	}
}

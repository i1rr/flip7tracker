package config

import (
	"log/slog"
	"strings"
	"testing"
)

// validEnv is a complete, valid set of environment values. Tests start from a
// copy and mutate individual keys to exercise failure and default paths.
func validEnv() map[string]string {
	return map[string]string{
		"MM_URL":               "http://172.28.0.1:8065",
		"MM_BOT_TOKEN":         "tok-bot-secret",
		"MM_TEAM":              "dom1k",
		"MM_CHANNEL":           "flip7",
		"OWNER_USER_ID":        "owner123",
		"OWNER_USERNAME":       "rivan",
		"HMAC_KEY":             strings.Repeat("a", 64), // 64 bytes, well above the 32-byte floor
		"SLASH_TOKEN_FLIP7":    "stok-flip7",
		"INTEGRATION_BASE_URL": "http://172.28.0.5:8068",
		"DATABASE_URL":         "",
		"LISTEN_ADDR":          "",
		"LOG_LEVEL":            "",
	}
}

// applyEnv sets every key (including blanks) via t.Setenv so the process env is
// deterministic and auto-restored after the test.
func applyEnv(t *testing.T, env map[string]string) {
	t.Helper()
	for k, v := range env {
		t.Setenv(k, v)
	}
}

func TestFromEnv_Defaults(t *testing.T) {
	applyEnv(t, validEnv())

	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.DatabaseURL != DefaultDatabaseURL {
		t.Errorf("DatabaseURL = %q, want default %q", cfg.DatabaseURL, DefaultDatabaseURL)
	}
	if cfg.ListenAddr != DefaultListenAddr {
		t.Errorf("ListenAddr = %q, want default %q", cfg.ListenAddr, DefaultListenAddr)
	}
	if cfg.LogLevel != DefaultLogLevel {
		t.Errorf("LogLevel = %q, want default %q", cfg.LogLevel, DefaultLogLevel)
	}
}

func TestFromEnv_Override(t *testing.T) {
	env := validEnv()
	env["DATABASE_URL"] = "/tmp/flip7.db"
	env["LISTEN_ADDR"] = "0.0.0.0:9000"
	env["LOG_LEVEL"] = "debug"
	applyEnv(t, env)

	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.DatabaseURL != "/tmp/flip7.db" {
		t.Errorf("DatabaseURL override = %q", cfg.DatabaseURL)
	}
	if cfg.ListenAddr != "0.0.0.0:9000" {
		t.Errorf("ListenAddr override = %q", cfg.ListenAddr)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel override = %q", cfg.LogLevel)
	}
	if cfg.SlogLevel() != slog.LevelDebug {
		t.Errorf("SlogLevel = %v, want Debug", cfg.SlogLevel())
	}
}

func TestSlogLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"debug":       slog.LevelDebug,
		"info":        slog.LevelInfo,
		"warn":        slog.LevelWarn,
		"warning":     slog.LevelWarn,
		"error":       slog.LevelError,
		"":            slog.LevelInfo,
		"nonsense":    slog.LevelInfo,
		"  DEBUG    ": slog.LevelDebug,
	}
	for in, want := range cases {
		c := &Config{LogLevel: in}
		if got := c.SlogLevel(); got != want {
			t.Errorf("SlogLevel(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestFromEnv_MissingRequired(t *testing.T) {
	for _, key := range []string{
		"MM_URL", "MM_BOT_TOKEN", "MM_TEAM", "MM_CHANNEL", "HMAC_KEY",
		"SLASH_TOKEN_FLIP7", "INTEGRATION_BASE_URL",
	} {
		t.Run(key, func(t *testing.T) {
			env := validEnv()
			env[key] = "" // blank == missing for required vars
			applyEnv(t, env)

			_, err := FromEnv()
			if err == nil {
				t.Fatalf("expected error when %s is missing", key)
			}
			if !strings.Contains(err.Error(), key) {
				t.Errorf("error %q should name the missing var %q", err.Error(), key)
			}
		})
	}
}

func TestFromEnv_MissingRequiredListsAll(t *testing.T) {
	env := validEnv()
	env["MM_URL"] = ""
	env["MM_TEAM"] = ""
	applyEnv(t, env)

	_, err := FromEnv()
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "MM_URL") || !strings.Contains(err.Error(), "MM_TEAM") {
		t.Errorf("error should list every missing var, got %q", err.Error())
	}
}

func TestFromEnv_OwnerRequired(t *testing.T) {
	env := validEnv()
	env["OWNER_USER_ID"] = ""
	env["OWNER_USERNAME"] = ""
	applyEnv(t, env)

	_, err := FromEnv()
	if err == nil {
		t.Fatal("expected error when no owner identity is set")
	}
	if !strings.Contains(err.Error(), "OWNER_USER_ID") || !strings.Contains(err.Error(), "OWNER_USERNAME") {
		t.Errorf("error should mention owner vars, got %q", err.Error())
	}
}

func TestFromEnv_OwnerIDOnlyOK(t *testing.T) {
	env := validEnv()
	env["OWNER_USERNAME"] = ""
	applyEnv(t, env)
	if _, err := FromEnv(); err != nil {
		t.Fatalf("owner id alone should be valid: %v", err)
	}
}

func TestFromEnv_OwnerUsernameOnlyOK(t *testing.T) {
	env := validEnv()
	env["OWNER_USER_ID"] = ""
	applyEnv(t, env)
	if _, err := FromEnv(); err != nil {
		t.Fatalf("owner username alone should be valid: %v", err)
	}
}

func TestFromEnv_HMACKeyTooShort(t *testing.T) {
	env := validEnv()
	env["HMAC_KEY"] = strings.Repeat("a", 31) // one byte under the floor
	applyEnv(t, env)

	_, err := FromEnv()
	if err == nil {
		t.Fatal("expected error for short HMAC_KEY")
	}
	if !strings.Contains(err.Error(), "HMAC_KEY") || !strings.Contains(err.Error(), "32") {
		t.Errorf("error should explain the 32-byte HMAC_KEY floor, got %q", err.Error())
	}
}

func TestFromEnv_HMACKeyExactlyMinOK(t *testing.T) {
	env := validEnv()
	env["HMAC_KEY"] = strings.Repeat("a", 32)
	applyEnv(t, env)
	if _, err := FromEnv(); err != nil {
		t.Fatalf("32-byte HMAC_KEY should be accepted: %v", err)
	}
}

func TestRedacted_OmitsSecrets(t *testing.T) {
	applyEnv(t, validEnv())
	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	red := cfg.Redacted()

	// The map must not carry any secret values.
	for k, v := range red {
		s, ok := v.(string)
		if !ok {
			continue
		}
		if strings.Contains(s, cfg.MMBotToken) && cfg.MMBotToken != "" {
			t.Errorf("redacted[%s] leaks bot token", k)
		}
		if strings.Contains(s, cfg.HMACKey) {
			t.Errorf("redacted[%s] leaks HMAC key", k)
		}
		if strings.Contains(s, cfg.SlashTokenFlip7) && cfg.SlashTokenFlip7 != "" {
			t.Errorf("redacted[%s] leaks slash token", k)
		}
	}
	if red["hmac_key_set"] != true {
		t.Errorf("hmac_key_set should be true")
	}
	if red["mm_bot_token_set"] != true {
		t.Errorf("mm_bot_token_set should be true")
	}
	if red["slash_token_set"] != true {
		t.Errorf("slash_token_set should be true")
	}
}

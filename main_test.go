package main

import (
	"log/slog"
	"testing"
	"time"
)

// ---- getEnv ----

func TestGetEnv_ReturnsEnvValue(t *testing.T) {
	t.Setenv("TEST_KEY", "hello")
	if got := getEnv("TEST_KEY", "default"); got != "hello" {
		t.Errorf("expected %q, got %q", "hello", got)
	}
}

func TestGetEnv_ReturnsDefaultWhenMissing(t *testing.T) {
	t.Setenv("TEST_MISSING_KEY", "")
	if got := getEnv("TEST_MISSING_KEY", "default"); got != "default" {
		t.Errorf("expected %q, got %q", "default", got)
	}
}

func TestGetEnv_ReturnsDefaultWhenUnset(t *testing.T) {
	if got := getEnv("DEFINITELY_NOT_SET_XYZ", "fallback"); got != "fallback" {
		t.Errorf("expected %q, got %q", "fallback", got)
	}
}

// ---- parseLogLevel ----

func TestParseLogLevel(t *testing.T) {
	tests := []struct {
		input string
		want  slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"DEBUG", slog.LevelDebug},
		{"info", slog.LevelInfo},
		{"INFO", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"warning", slog.LevelWarn},
		{"WARNING", slog.LevelWarn},
		{"error", slog.LevelError},
		{"ERROR", slog.LevelError},
		{"unknown", slog.LevelInfo},
		{"", slog.LevelInfo},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			if got := parseLogLevel(tc.input); got != tc.want {
				t.Errorf("parseLogLevel(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

// ---- loadConfig ----

func TestLoadConfig_Defaults(t *testing.T) {
	// Clear any env vars that loadConfig reads so we get pure defaults.
	for _, key := range []string{
		"POLL_INTERVAL", "REDIS_DB", "MAX_REPLIES",
		"LOG_LEVEL", "REDIS_ADDR", "REDIS_PASSWORD",
		"REDIS_SORTED_SET", "REDIS_CHANNEL", "REDIS_REACTION_LIST",
		"SLACK_BOT_TOKEN",
	} {
		t.Setenv(key, "")
	}

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() unexpected error: %v", err)
	}

	if cfg.RedisAddr != "localhost:6379" {
		t.Errorf("RedisAddr = %q, want %q", cfg.RedisAddr, "localhost:6379")
	}
	if cfg.RedisDB != 0 {
		t.Errorf("RedisDB = %d, want 0", cfg.RedisDB)
	}
	if cfg.PollInterval != 10*time.Second {
		t.Errorf("PollInterval = %v, want 10s", cfg.PollInterval)
	}
	if cfg.MaxReplies != DefaultMaxReplies {
		t.Errorf("MaxReplies = %d, want %d", cfg.MaxReplies, DefaultMaxReplies)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel = %v, want Info", cfg.LogLevel)
	}
}

func TestLoadConfig_CustomValues(t *testing.T) {
	t.Setenv("REDIS_ADDR", "redis.example.com:6380")
	t.Setenv("REDIS_DB", "3")
	t.Setenv("POLL_INTERVAL", "30s")
	t.Setenv("MAX_REPLIES", "50")
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("SLACK_BOT_TOKEN", "xoxb-test")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() unexpected error: %v", err)
	}

	if cfg.RedisAddr != "redis.example.com:6380" {
		t.Errorf("RedisAddr = %q", cfg.RedisAddr)
	}
	if cfg.RedisDB != 3 {
		t.Errorf("RedisDB = %d, want 3", cfg.RedisDB)
	}
	if cfg.PollInterval != 30*time.Second {
		t.Errorf("PollInterval = %v, want 30s", cfg.PollInterval)
	}
	if cfg.MaxReplies != 50 {
		t.Errorf("MaxReplies = %d, want 50", cfg.MaxReplies)
	}
	if cfg.LogLevel != slog.LevelDebug {
		t.Errorf("LogLevel = %v, want Debug", cfg.LogLevel)
	}
	if cfg.SlackBotToken != "xoxb-test" {
		t.Errorf("SlackBotToken = %q", cfg.SlackBotToken)
	}
}

func TestLoadConfig_InvalidPollInterval(t *testing.T) {
	t.Setenv("POLL_INTERVAL", "not-a-duration")
	t.Setenv("REDIS_DB", "")
	t.Setenv("MAX_REPLIES", "")

	_, err := loadConfig()
	if err == nil {
		t.Fatal("expected error for invalid POLL_INTERVAL, got nil")
	}
}

func TestLoadConfig_InvalidRedisDB(t *testing.T) {
	t.Setenv("POLL_INTERVAL", "")
	t.Setenv("REDIS_DB", "abc")
	t.Setenv("MAX_REPLIES", "")

	_, err := loadConfig()
	if err == nil {
		t.Fatal("expected error for invalid REDIS_DB, got nil")
	}
}

func TestLoadConfig_InvalidMaxReplies(t *testing.T) {
	t.Setenv("POLL_INTERVAL", "")
	t.Setenv("REDIS_DB", "")
	t.Setenv("MAX_REPLIES", "not-a-number")

	_, err := loadConfig()
	if err == nil {
		t.Fatal("expected error for invalid MAX_REPLIES, got nil")
	}
}

func TestLoadConfig_MaxRepliesZero(t *testing.T) {
	t.Setenv("POLL_INTERVAL", "")
	t.Setenv("REDIS_DB", "")
	t.Setenv("MAX_REPLIES", "0")

	_, err := loadConfig()
	if err == nil {
		t.Fatal("expected error for MAX_REPLIES=0, got nil")
	}
}

func TestLoadConfig_MaxRepliesNegative(t *testing.T) {
	t.Setenv("POLL_INTERVAL", "")
	t.Setenv("REDIS_DB", "")
	t.Setenv("MAX_REPLIES", "-1")

	_, err := loadConfig()
	if err == nil {
		t.Fatal("expected error for negative MAX_REPLIES, got nil")
	}
}

func TestLoadConfig_MaxRepliesExceedsSlackLimit(t *testing.T) {
	t.Setenv("POLL_INTERVAL", "")
	t.Setenv("REDIS_DB", "")
	t.Setenv("MAX_REPLIES", "1001")

	_, err := loadConfig()
	if err == nil {
		t.Fatal("expected error for MAX_REPLIES > SlackMaxRepliesLimit, got nil")
	}
}

// ---- validateTimeBombMessage ----

func TestValidateTimeBombMessage_Valid(t *testing.T) {
	msg := &TimeBombMessage{Channel: "C123", TS: "1234567890.000001", TTL: 60}
	if err := validateTimeBombMessage(msg); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateTimeBombMessage_MissingChannel(t *testing.T) {
	msg := &TimeBombMessage{Channel: "", TS: "1234567890.000001", TTL: 60}
	if err := validateTimeBombMessage(msg); err == nil {
		t.Error("expected error for missing channel, got nil")
	}
}

func TestValidateTimeBombMessage_MissingTS(t *testing.T) {
	msg := &TimeBombMessage{Channel: "C123", TS: "", TTL: 60}
	if err := validateTimeBombMessage(msg); err == nil {
		t.Error("expected error for missing ts, got nil")
	}
}

func TestValidateTimeBombMessage_ZeroTTL(t *testing.T) {
	msg := &TimeBombMessage{Channel: "C123", TS: "1234567890.000001", TTL: 0}
	if err := validateTimeBombMessage(msg); err == nil {
		t.Error("expected error for TTL=0, got nil")
	}
}

func TestValidateTimeBombMessage_NegativeTTL(t *testing.T) {
	msg := &TimeBombMessage{Channel: "C123", TS: "1234567890.000001", TTL: -1}
	if err := validateTimeBombMessage(msg); err == nil {
		t.Error("expected error for negative TTL, got nil")
	}
}

func TestValidateTimeBombMessage_TTLAtMaximum(t *testing.T) {
	msg := &TimeBombMessage{Channel: "C123", TS: "1234567890.000001", TTL: MaxTTL}
	if err := validateTimeBombMessage(msg); err != nil {
		t.Errorf("unexpected error for TTL=MaxTTL: %v", err)
	}
}

func TestValidateTimeBombMessage_TTLExceedsMaximum(t *testing.T) {
	msg := &TimeBombMessage{Channel: "C123", TS: "1234567890.000001", TTL: MaxTTL + 1}
	if err := validateTimeBombMessage(msg); err == nil {
		t.Error("expected error for TTL > MaxTTL, got nil")
	}
}

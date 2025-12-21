package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/slack-go/slack"
)

// Message represents the structure of a message in Redis
type Message struct {
	Channel string `json:"channel"`
	TS      string `json:"ts"`
}

// Config holds the application configuration
type Config struct {
	RedisAddr      string
	RedisPassword  string
	RedisDB        int
	RedisSortedSet string
	SlackBotToken  string
	PollInterval   time.Duration
	LogLevel       slog.Level
}

func loadConfig() (*Config, error) {
	pollInterval, err := time.ParseDuration(getEnv("POLL_INTERVAL", "10s"))
	if err != nil {
		return nil, fmt.Errorf("invalid POLL_INTERVAL: %w", err)
	}

	redisDB, err := strconv.Atoi(getEnv("REDIS_DB", "0"))
	if err != nil {
		return nil, fmt.Errorf("invalid REDIS_DB: %w", err)
	}

	logLevel := parseLogLevel(getEnv("LOG_LEVEL", "info"))

	return &Config{
		RedisAddr:      getEnv("REDIS_ADDR", "localhost:6379"),
		RedisPassword:  getEnv("REDIS_PASSWORD", ""),
		RedisDB:        redisDB,
		RedisSortedSet: getEnv("REDIS_SORTED_SET", "delays"),
		SlackBotToken:  getEnv("SLACK_BOT_TOKEN", ""),
		PollInterval:   pollInterval,
		LogLevel:       logLevel,
	}, nil
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func parseLogLevel(level string) slog.Level {
	switch strings.ToLower(level) {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func createLogger(level slog.Level) *slog.Logger {
	handler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: level,
	})
	return slog.New(handler)
}

type TimeBombService struct {
	redis  *redis.Client
	slack  *slack.Client
	config *Config
	logger *slog.Logger
}

func NewTimeBombService(config *Config) *TimeBombService {
	redisClient := redis.NewClient(&redis.Options{
		Addr:     config.RedisAddr,
		Password: config.RedisPassword,
		DB:       config.RedisDB,
	})

	slackClient := slack.New(config.SlackBotToken)

	// Create logger with configured level
	logger := createLogger(config.LogLevel)

	return &TimeBombService{
		redis:  redisClient,
		slack:  slackClient,
		config: config,
		logger: logger,
	}
}

func (s *TimeBombService) Start(ctx context.Context) error {
	// Test connections
	if err := s.testConnections(ctx); err != nil {
		return fmt.Errorf("connection test failed: %w", err)
	}

	s.logger.Info("TimeBomb service started successfully")
	s.logger.Info("Configuration",
		"poll_interval", s.config.PollInterval,
		"redis_sorted_set", s.config.RedisSortedSet)

	ticker := time.NewTicker(s.config.PollInterval)
	defer ticker.Stop()

	// Run immediately on start
	if err := s.processExpiredMessages(ctx); err != nil {
		s.logger.Error("Error processing expired messages", "error", err)
	}

	for {
		select {
		case <-ctx.Done():
			s.logger.Info("Shutting down TimeBomb service...")
			return ctx.Err()
		case <-ticker.C:
			if err := s.processExpiredMessages(ctx); err != nil {
				s.logger.Error("Error processing expired messages", "error", err)
			}
		}
	}
}

func (s *TimeBombService) testConnections(ctx context.Context) error {
	// Test Redis connection
	if err := s.redis.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("redis connection failed: %w", err)
	}
	s.logger.Info("Redis connection successful")

	// Test Slack connection
	if _, err := s.slack.AuthTestContext(ctx); err != nil {
		return fmt.Errorf("slack authentication failed: %w", err)
	}
	s.logger.Info("Slack authentication successful")

	return nil
}

func (s *TimeBombService) processExpiredMessages(ctx context.Context) error {
	now := time.Now().Unix()

	// Get messages with scores from 0 to current timestamp
	results, err := s.redis.ZRangeByScoreWithScores(ctx, s.config.RedisSortedSet, &redis.ZRangeBy{
		Min: "0",
		Max: fmt.Sprintf("%d", now),
	}).Result()

	if err != nil {
		return fmt.Errorf("failed to query redis: %w", err)
	}

	if len(results) == 0 {
		s.logger.Debug("No expired messages found")
		return nil
	}

	s.logger.Info("Found expired messages", "count", len(results))

	for _, result := range results {
		// Safe type assertion
		member, ok := result.Member.(string)
		if !ok {
			s.logger.Error("Invalid member type in sorted set",
				"expected", "string",
				"got", fmt.Sprintf("%T", result.Member))
			// Remove invalid entry from Redis
			if err := s.redis.ZRem(ctx, s.config.RedisSortedSet, result.Member).Err(); err != nil {
				s.logger.Error("Error removing invalid entry from redis", "error", err)
			}
			continue
		}

		if err := s.processMessage(ctx, member); err != nil {
			s.logger.Error("Error processing message", "error", err)
			continue
		}
	}

	return nil
}

func (s *TimeBombService) processMessage(ctx context.Context, payload string) error {
	var msg Message
	if err := json.Unmarshal([]byte(payload), &msg); err != nil {
		s.logger.Warn("Failed to unmarshal message, removing from queue", "error", err)
		// Invalid JSON - remove from Redis so it doesn't get retried
		if removeErr := s.redis.ZRem(ctx, s.config.RedisSortedSet, payload).Err(); removeErr != nil {
			s.logger.Error("Error removing invalid message from redis", "error", removeErr)
		}
		return fmt.Errorf("failed to unmarshal message: %w", err)
	}

	s.logger.Info("Deleting message", "channel", msg.Channel, "ts", msg.TS)

	// Delete the message from Slack
	_, _, err := s.slack.DeleteMessageContext(ctx, msg.Channel, msg.TS)
	if err != nil {
		// Check if it's a permanent error (message not found, channel not found, etc.)
		slackErr, ok := err.(slack.SlackErrorResponse)
		if ok && (slackErr.Err == "message_not_found" || slackErr.Err == "channel_not_found" || slackErr.Err == "not_in_channel") {
			// Permanent error - remove from Redis
			s.logger.Warn("Message cannot be deleted (permanent error), removing from queue",
				"error", slackErr.Err)
			if removeErr := s.redis.ZRem(ctx, s.config.RedisSortedSet, payload).Err(); removeErr != nil {
				s.logger.Error("Error removing message from redis", "error", removeErr)
			}
			return fmt.Errorf("permanent error deleting slack message: %w", err)
		}
		// Transient error - leave in Redis for retry
		return fmt.Errorf("failed to delete slack message (will retry): %w", err)
	}

	s.logger.Info("Successfully deleted message", "channel", msg.Channel, "ts", msg.TS)

	// Remove the message from Redis
	if err := s.redis.ZRem(ctx, s.config.RedisSortedSet, payload).Err(); err != nil {
		return fmt.Errorf("failed to remove message from redis: %w", err)
	}

	s.logger.Debug("Removed message from Redis sorted set")

	return nil
}

func (s *TimeBombService) Close() error {
	if err := s.redis.Close(); err != nil {
		return fmt.Errorf("failed to close redis client: %w", err)
	}
	return nil
}

func main() {
	// Create default logger for early error reporting
	logger := createLogger(slog.LevelInfo)

	config, err := loadConfig()
	if err != nil {
		logger.Error("Failed to load configuration", "error", err)
		os.Exit(1)
	}

	// Update logger with configured level
	logger = createLogger(config.LogLevel)

	if config.SlackBotToken == "" {
		logger.Error("SLACK_BOT_TOKEN environment variable is required")
		os.Exit(1)
	}

	service := NewTimeBombService(config)
	defer service.Close()

	// Set up context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Set up signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// Start service in a goroutine
	errChan := make(chan error, 1)
	go func() {
		errChan <- service.Start(ctx)
	}()

	// Wait for shutdown signal or error
	select {
	case sig := <-sigChan:
		service.logger.Info("Received signal", "signal", sig)
		cancel()
		// Give the service time to clean up
		time.Sleep(1 * time.Second)
	case err := <-errChan:
		if err != nil && err != context.Canceled {
			service.logger.Error("Service error", "error", err)
			os.Exit(1)
		}
	}

	service.logger.Info("TimeBomb service stopped")
}

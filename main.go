package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
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

	return &Config{
		RedisAddr:      getEnv("REDIS_ADDR", "localhost:6379"),
		RedisPassword:  getEnv("REDIS_PASSWORD", ""),
		RedisDB:        redisDB,
		RedisSortedSet: getEnv("REDIS_SORTED_SET", "delays"),
		SlackBotToken:  getEnv("SLACK_BOT_TOKEN", ""),
		PollInterval:   pollInterval,
	}, nil
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

type TimeBombService struct {
	redis  *redis.Client
	slack  *slack.Client
	config *Config
}

func NewTimeBombService(config *Config) *TimeBombService {
	redisClient := redis.NewClient(&redis.Options{
		Addr:     config.RedisAddr,
		Password: config.RedisPassword,
		DB:       config.RedisDB,
	})

	slackClient := slack.New(config.SlackBotToken)

	return &TimeBombService{
		redis:  redisClient,
		slack:  slackClient,
		config: config,
	}
}

func (s *TimeBombService) Start(ctx context.Context) error {
	// Test connections
	if err := s.testConnections(ctx); err != nil {
		return fmt.Errorf("connection test failed: %w", err)
	}

	log.Println("TimeBomb service started successfully")
	log.Printf("Polling interval: %v", s.config.PollInterval)
	log.Printf("Redis sorted set: %s", s.config.RedisSortedSet)

	ticker := time.NewTicker(s.config.PollInterval)
	defer ticker.Stop()

	// Run immediately on start
	if err := s.processExpiredMessages(ctx); err != nil {
		log.Printf("Error processing expired messages: %v", err)
	}

	for {
		select {
		case <-ctx.Done():
			log.Println("Shutting down TimeBomb service...")
			return ctx.Err()
		case <-ticker.C:
			if err := s.processExpiredMessages(ctx); err != nil {
				log.Printf("Error processing expired messages: %v", err)
			}
		}
	}
}

func (s *TimeBombService) testConnections(ctx context.Context) error {
	// Test Redis connection
	if err := s.redis.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("redis connection failed: %w", err)
	}
	log.Println("Redis connection successful")

	// Test Slack connection
	if _, err := s.slack.AuthTestContext(ctx); err != nil {
		return fmt.Errorf("slack authentication failed: %w", err)
	}
	log.Println("Slack authentication successful")

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
		log.Printf("No expired messages found")
		return nil
	}

	log.Printf("Found %d expired message(s)", len(results))

	for _, result := range results {
		if err := s.processMessage(ctx, result.Member.(string)); err != nil {
			log.Printf("Error processing message: %v", err)
			continue
		}
	}

	return nil
}

func (s *TimeBombService) processMessage(ctx context.Context, payload string) error {
	var msg Message
	if err := json.Unmarshal([]byte(payload), &msg); err != nil {
		return fmt.Errorf("failed to unmarshal message: %w", err)
	}

	log.Printf("Deleting message from channel %s with ts %s", msg.Channel, msg.TS)

	// Delete the message from Slack
	_, _, err := s.slack.DeleteMessageContext(ctx, msg.Channel, msg.TS)
	if err != nil {
		return fmt.Errorf("failed to delete slack message: %w", err)
	}

	log.Printf("Successfully deleted message from channel %s with ts %s", msg.Channel, msg.TS)

	// Remove the message from Redis
	if err := s.redis.ZRem(ctx, s.config.RedisSortedSet, payload).Err(); err != nil {
		return fmt.Errorf("failed to remove message from redis: %w", err)
	}

	log.Printf("Removed message from Redis sorted set")

	return nil
}

func (s *TimeBombService) Close() error {
	if err := s.redis.Close(); err != nil {
		return fmt.Errorf("failed to close redis client: %w", err)
	}
	return nil
}

func main() {
	config, err := loadConfig()
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	if config.SlackBotToken == "" {
		log.Fatal("SLACK_BOT_TOKEN environment variable is required")
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
		log.Printf("Received signal: %v", sig)
		cancel()
		// Give the service time to clean up
		time.Sleep(1 * time.Second)
	case err := <-errChan:
		if err != nil && err != context.Canceled {
			log.Fatalf("Service error: %v", err)
		}
	}

	log.Println("TimeBomb service stopped")
}

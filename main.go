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

const (
	// MaxTTL is the maximum allowed TTL in seconds (~68 years)
	// Using math.MaxInt32 ensures no overflow when converting to time.Duration
	// (time.Duration max is ~292 years, so 68 years is well within limits)
	MaxTTL = 2147483647 // math.MaxInt32

	// MaxRepliesPerRequest is the maximum number of replies to fetch per API request
	MaxRepliesPerRequest = 1000

	// Slack API error constants
	slackErrMessageNotFound = "message_not_found"
	slackErrChannelNotFound = "channel_not_found"
	slackErrThreadNotFound  = "thread_not_found"
	slackErrNotInChannel    = "not_in_channel"
)

// Message represents the structure of a message in Redis sorted set (internal use)
type Message struct {
	Channel string `json:"channel"`
	TS      string `json:"ts"`
}

// ReactionMessage represents the structure of a reaction message to be posted to SlackLiner
type ReactionMessage struct {
	Reaction string `json:"reaction"`
	Channel  string `json:"channel"`
	TS       string `json:"ts"`
}

// TimeBombMessage represents the structure of messages received via Redis Pub/Sub
type TimeBombMessage struct {
	Channel string `json:"channel"`
	TS      string `json:"ts"`
	TTL     int    `json:"ttl"`
}

// Config holds the application configuration
type Config struct {
	RedisAddr         string
	RedisPassword     string
	RedisDB           int
	RedisSortedSet    string
	RedisChannel      string
	RedisReactionList string
	SlackBotToken     string
	PollInterval      time.Duration
	LogLevel          slog.Level
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
		RedisAddr:         getEnv("REDIS_ADDR", "localhost:6379"),
		RedisPassword:     getEnv("REDIS_PASSWORD", ""),
		RedisDB:           redisDB,
		RedisSortedSet:    getEnv("REDIS_SORTED_SET", "delays"),
		RedisChannel:      getEnv("REDIS_CHANNEL", "timebomb-messages"),
		RedisReactionList: getEnv("REDIS_REACTION_LIST", "slack_reactions"),
		SlackBotToken:     getEnv("SLACK_BOT_TOKEN", ""),
		PollInterval:      pollInterval,
		LogLevel:          logLevel,
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
		"redis_sorted_set", s.config.RedisSortedSet,
		"redis_channel", s.config.RedisChannel)

	// Start Redis Pub/Sub subscriber in a separate goroutine
	go s.subscribeToChannel(ctx)

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

func (s *TimeBombService) subscribeToChannel(ctx context.Context) {
	retryDelay := 5 * time.Second
	const maxRetryDelay = 60 * time.Second

	for {
		if ctx.Err() != nil {
			return
		}

		pubsub := s.redis.Subscribe(ctx, s.config.RedisChannel)

		s.logger.Info("Attempting to subscribe to Redis channel", "channel", s.config.RedisChannel)

		// Wait for confirmation that subscription is created
		_, err := pubsub.Receive(ctx)
		if err != nil {
			s.logger.Error("Error receiving subscription confirmation", "error", err)
			pubsub.Close()

			// Check if context is done
			if ctx.Err() != nil {
				return
			}

			// Retry with exponential backoff (capped at maxRetryDelay)
			s.logger.Info("Retrying subscription", "after", retryDelay)
			time.Sleep(retryDelay)
			retryDelay *= 2
			if retryDelay > maxRetryDelay {
				retryDelay = maxRetryDelay
			}
			continue
		}

		// Successfully subscribed, start processing messages
		s.logger.Info("Successfully subscribed to Redis channel", "channel", s.config.RedisChannel)
		// Reset retry delay after successful connection
		retryDelay = 5 * time.Second

		// Get the channel for receiving messages
		ch := pubsub.Channel()

		// Process messages until context is done or channel closes
		channelClosed := false
		for !channelClosed {
			select {
			case <-ctx.Done():
				s.logger.Info("Stopping Redis channel subscription")
				pubsub.Close()
				return
			case msg, ok := <-ch:
				if !ok {
					// Channel closed - connection lost
					s.logger.Warn("Redis Pub/Sub channel closed unexpectedly, will attempt to reconnect")
					channelClosed = true
					break
				}
				if msg == nil {
					s.logger.Warn("Received nil message from channel")
					continue
				}
				if err := s.handleIncomingMessage(ctx, msg.Payload); err != nil {
					s.logger.Error("Error handling incoming message", "error", err)
				}
			}
		}

		// Close pubsub and retry connection
		pubsub.Close()
	}
}

func (s *TimeBombService) handleIncomingMessage(ctx context.Context, payload string) error {
	var tbMsg TimeBombMessage
	if err := json.Unmarshal([]byte(payload), &tbMsg); err != nil {
		s.logger.Warn("Failed to unmarshal TimeBombMessage", "error", err)
		return fmt.Errorf("failed to unmarshal TimeBombMessage: %w", err)
	}

	// Validate required fields
	if tbMsg.Channel == "" {
		s.logger.Warn("Missing required field: channel")
		return fmt.Errorf("channel field is required")
	}
	if tbMsg.TS == "" {
		s.logger.Warn("Missing required field: ts")
		return fmt.Errorf("ts field is required")
	}

	// Validate TTL
	if tbMsg.TTL <= 0 {
		s.logger.Warn("Invalid TTL value (must be positive)", "ttl", tbMsg.TTL)
		return fmt.Errorf("TTL must be positive, got %d", tbMsg.TTL)
	}

	// Prevent integer overflow - max TTL is ~68 years in seconds
	if tbMsg.TTL > MaxTTL {
		s.logger.Warn("TTL value too large", "ttl", tbMsg.TTL)
		return fmt.Errorf("TTL too large, max is %d seconds", MaxTTL)
	}

	s.logger.Info("Received message from channel",
		"channel", tbMsg.Channel,
		"ts", tbMsg.TS,
		"ttl", tbMsg.TTL)

	// Calculate expiration timestamp using time.Add to avoid overflow
	now := time.Now()
	expirationTime := now.Add(time.Duration(tbMsg.TTL) * time.Second).Unix()

	// Create internal message format for sorted set
	internalMsg := Message{
		Channel: tbMsg.Channel,
		TS:      tbMsg.TS,
	}

	// Marshal internal message to JSON
	msgJSON, err := json.Marshal(internalMsg)
	if err != nil {
		s.logger.Error("Failed to marshal internal message", "error", err)
		return fmt.Errorf("failed to marshal internal message: %w", err)
	}

	// Add to sorted set with expiration time as score
	if err := s.redis.ZAdd(ctx, s.config.RedisSortedSet, redis.Z{
		Score:  float64(expirationTime),
		Member: string(msgJSON),
	}).Err(); err != nil {
		s.logger.Error("Failed to add message to sorted set", "error", err)
		return fmt.Errorf("failed to add message to sorted set: %w", err)
	}

	s.logger.Debug("Added message to sorted set",
		"sorted_set", s.config.RedisSortedSet,
		"expiration_time", expirationTime)

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

	s.logger.Info("Processing expired message", "channel", msg.Channel, "ts", msg.TS)

	// Remove from sorted set immediately to prevent duplicate processing
	if err := s.redis.ZRem(ctx, s.config.RedisSortedSet, payload).Err(); err != nil {
		s.logger.Error("Failed to remove message from redis", "error", err)
		return fmt.Errorf("failed to remove message from redis: %w", err)
	}

	// Post boom reaction to Redis list for SlackLiner
	reactionMsg := ReactionMessage{
		Reaction: "boom",
		Channel:  msg.Channel,
		TS:       msg.TS,
	}

	reactionJSON, err := json.Marshal(reactionMsg)
	if err != nil {
		s.logger.Error("Failed to marshal reaction message", "error", err)
		// Continue with deletion even if reaction fails
	} else {
		if err := s.redis.RPush(ctx, s.config.RedisReactionList, reactionJSON).Err(); err != nil {
			s.logger.Error("Failed to push reaction to Redis list", "error", err)
			// Continue with deletion even if reaction fails
		} else {
			s.logger.Info("Posted boom reaction", "channel", msg.Channel, "ts", msg.TS)
		}
	}

	// Asynchronously wait and delete message to avoid blocking the processing loop
	go s.deleteMessageAfterDelay(ctx, msg)

	return nil
}

func (s *TimeBombService) deleteMessageAfterDelay(ctx context.Context, msg Message) {
	// Wait 1 second before deleting (with context awareness for graceful shutdown)
	timer := time.NewTimer(1 * time.Second)
	select {
	case <-timer.C:
		// Timer expired, continue with deletion
	case <-ctx.Done():
		timer.Stop()
		s.logger.Info("Context cancelled during wait, skipping message deletion", "channel", msg.Channel, "ts", msg.TS)
		return
	}

	s.logger.Info("Deleting message", "channel", msg.Channel, "ts", msg.TS)

	// First, check and delete any replies to this message
	if err := s.deleteMessageReplies(ctx, msg.Channel, msg.TS); err != nil {
		s.logger.Error("Failed to delete message replies", "error", err, "channel", msg.Channel, "ts", msg.TS)
		// Continue with parent message deletion even if replies deletion fails
	}

	// Delete the message from Slack
	_, _, err := s.slack.DeleteMessageContext(ctx, msg.Channel, msg.TS)
	if err != nil {
		// Check if it's a permanent error (message not found, channel not found, etc.)
		slackErr, ok := err.(slack.SlackErrorResponse)
		if ok && (slackErr.Err == slackErrMessageNotFound || slackErr.Err == slackErrChannelNotFound || slackErr.Err == slackErrNotInChannel) {
			// Permanent error - log but message already removed from queue
			s.logger.Warn("Message cannot be deleted (permanent error)",
				"error", slackErr.Err,
				"channel", msg.Channel,
				"ts", msg.TS)
			return
		}
		// Transient error - message already removed from queue, so it won't retry
		s.logger.Error("Failed to delete slack message (message already removed from queue, cannot retry)",
			"error", err,
			"channel", msg.Channel,
			"ts", msg.TS)
		return
	}

	s.logger.Info("Successfully deleted message", "channel", msg.Channel, "ts", msg.TS)
}

// deleteMessageReplies fetches and deletes all replies to a message
func (s *TimeBombService) deleteMessageReplies(ctx context.Context, channel, ts string) error {
	// Fetch all replies to the message
	params := &slack.GetConversationRepliesParameters{
		ChannelID: channel,
		Timestamp: ts,
		Limit:     MaxRepliesPerRequest,
	}

	allReplies := make([]slack.Message, 0, MaxRepliesPerRequest)
	cursor := ""

	// Paginate through all replies
	for {
		params.Cursor = cursor
		msgs, hasMore, nextCursor, err := s.slack.GetConversationRepliesContext(ctx, params)
		if err != nil {
			// Check if it's a permanent error
			slackErr, ok := err.(slack.SlackErrorResponse)
			if ok && (slackErr.Err == slackErrThreadNotFound || slackErr.Err == slackErrMessageNotFound || slackErr.Err == slackErrChannelNotFound) {
				// Thread doesn't exist or message not found - nothing to delete
				s.logger.Debug("No replies found or thread doesn't exist", "channel", channel, "ts", ts)
				return nil
			}
			return fmt.Errorf("failed to get conversation replies: %w", err)
		}

		allReplies = append(allReplies, msgs...)

		if !hasMore {
			break
		}
		cursor = nextCursor
	}

	// The first message in the reply list is the parent message itself
	// We skip it and only delete the actual replies
	replyCount := 0
	for _, reply := range allReplies {
		// Skip the parent message (its timestamp matches the thread timestamp)
		if reply.Timestamp == ts {
			continue
		}

		s.logger.Debug("Deleting reply", "channel", channel, "ts", reply.Timestamp, "thread_ts", ts)

		_, _, err := s.slack.DeleteMessageContext(ctx, channel, reply.Timestamp)
		if err != nil {
			// Log error but continue deleting other replies
			slackErr, ok := err.(slack.SlackErrorResponse)
			if ok && (slackErr.Err == slackErrMessageNotFound || slackErr.Err == slackErrChannelNotFound || slackErr.Err == slackErrNotInChannel) {
				s.logger.Warn("Reply message not found, skipping", "channel", channel, "ts", reply.Timestamp)
				continue
			}
			s.logger.Error("Failed to delete reply", "error", err, "channel", channel, "ts", reply.Timestamp)
			continue
		}

		replyCount++
		s.logger.Debug("Successfully deleted reply", "channel", channel, "ts", reply.Timestamp)
	}

	if replyCount > 0 {
		s.logger.Info("Deleted message replies", "count", replyCount, "channel", channel, "parent_ts", ts)
	}

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

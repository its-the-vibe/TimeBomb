# TimeBomb

Service which polls for expired slack messages and deletes them.

## Overview

TimeBomb is a Go service that monitors a Redis sorted set for expired Slack messages and automatically deletes them using the Slack API. Messages are stored in Redis with their expiration timestamps, and this service polls the sorted set at regular intervals to find and delete expired messages.

## Features

- Polls Redis sorted set for expired messages
- Deletes messages from Slack channels using the Slack API
- Configurable via environment variables
- Graceful shutdown handling
- Docker and Docker Compose support
- Minimal "from scratch" runtime image

## Prerequisites

- Go 1.24+ (for local development)
- Docker and Docker Compose (for containerized deployment)
- Redis server (hosted externally)
- Slack Bot Token with appropriate permissions

## Configuration

The service is configured using environment variables:

### Required

- `SLACK_BOT_TOKEN`: Your Slack bot token (required for Slack API access)

### Optional

- `REDIS_ADDR`: Redis server address (default: `localhost:6379`)
- `REDIS_PASSWORD`: Redis password (default: empty)
- `REDIS_DB`: Redis database number (default: `0`)
- `REDIS_SORTED_SET`: Name of the sorted set containing messages (default: `delays`)
- `POLL_INTERVAL`: How often to poll for expired messages (default: `10s`)
- `LOG_LEVEL`: Logging level - `debug`, `info`, `warn`, or `error` (default: `info`)

## Message Format

Messages in the Redis sorted set should be JSON objects with the following structure:

```json
{
  "channel": "C0A43V03EBV",
  "ts": "1766268151.996789"
}
```

Where:
- `channel`: Slack channel ID
- `ts`: Slack message timestamp

## Logging

The service uses structured logging with configurable log levels via the `LOG_LEVEL` environment variable.

### Log Levels

- `debug`: Detailed information for debugging (includes "No expired messages found", Redis operations)
- `info`: General informational messages (service start/stop, message processing)
- `warn`: Warning messages (invalid messages, permanent Slack errors)
- `error`: Error messages (Redis failures, critical errors)

### Example

```bash
# Run with debug logging to see all messages
LOG_LEVEL=debug go run main.go

# Run with error logging to only see critical issues
LOG_LEVEL=error go run main.go
```

## Adding Messages to Redis

Messages are added to the sorted set using the `ZADD` command, where the score is the expiration timestamp:

```bash
ZADD delays <expire_timestamp> '{"channel":"C0A43V03EBV","ts":"1766268151.996789"}'
```

Example:
```bash
# Message expires at Unix timestamp 1766268151
ZADD delays 1766268151 '{"channel":"C0A43V03EBV","ts":"1766268151.996789"}'
```

## Running Locally

### Quick Start with Makefile

The project includes a Makefile for common operations:

```bash
# Show available commands
make help

# Build the binary
make build

# Run locally
make run

# Run linters
make lint

# Build Docker image
make docker-build

# Run with Docker Compose
make docker-run
```

### Using Go

1. Copy the example environment file:
```bash
cp .env.example .env
```

2. Edit `.env` and set your configuration:
```bash
SLACK_BOT_TOKEN=xoxb-your-token-here
REDIS_ADDR=localhost:6379
```

3. Run the service:
```bash
go run main.go
# or
make run
```

### Using Docker

1. Build the image:
```bash
docker build -t timebomb .
```

2. Run the container:
```bash
docker run -e SLACK_BOT_TOKEN=xoxb-your-token-here \
           -e REDIS_ADDR=your-redis-host:6379 \
           timebomb
```

### Using Docker Compose

1. Create a `.env` file with your configuration:
```bash
SLACK_BOT_TOKEN=xoxb-your-token-here
REDIS_ADDR=your-redis-host:6379
REDIS_PASSWORD=your-redis-password
POLL_INTERVAL=10s
```

2. Start the service:
```bash
docker-compose up -d
```

3. View logs:
```bash
docker-compose logs -f timebomb
```

4. Stop the service:
```bash
docker-compose down
```

## How It Works

1. The service connects to Redis and Slack on startup
2. Every `POLL_INTERVAL`, it queries the Redis sorted set for messages with scores between 0 and the current Unix timestamp
3. For each expired message:
   - The message is parsed from JSON
   - The message is deleted from Slack using the API
   - The message is removed from the Redis sorted set
4. The service continues polling until it receives a shutdown signal (SIGINT or SIGTERM)
5. On shutdown, the service gracefully closes connections and exits

## Slack Bot Permissions

Your Slack bot needs the following OAuth scopes:
- `chat:write` - To delete messages
- `channels:history` - To access channel messages
- `groups:history` - To access private channel messages (if needed)

## Development

### Building

```bash
go build -o timebomb .
```

### Testing the Service

You can test the service by adding a test message to Redis that expires soon:

```bash
# Add a message that expires in 30 seconds
EXPIRE_TIME=$(($(date +%s) + 30))
redis-cli ZADD delays $EXPIRE_TIME '{"channel":"YOUR_CHANNEL_ID","ts":"YOUR_MESSAGE_TS"}'
```

## Graceful Shutdown

The service handles SIGINT and SIGTERM signals for graceful shutdown. When a shutdown signal is received:
1. The polling loop is stopped
2. Redis connections are closed
3. The service exits cleanly

## Troubleshooting

### Service won't start
- Verify `SLACK_BOT_TOKEN` is set and valid
- Check Redis connection settings
- Ensure the Slack bot has proper permissions

### Messages not being deleted
- Verify the message format in Redis is correct JSON
- Check that the bot is a member of the channel
- Verify the message timestamp is correct
- Check service logs for error messages

## License

MIT

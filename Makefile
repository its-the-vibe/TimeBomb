.PHONY: build run clean docker-build docker-run test lint help

# Build the binary
build:
	@echo "Building TimeBomb..."
	@go build -o timebomb .

# Run the service locally
run:
	@echo "Running TimeBomb..."
	@go run main.go

# Clean build artifacts
clean:
	@echo "Cleaning..."
	@rm -f timebomb
	@go clean

# Build Docker image
docker-build:
	@echo "Building Docker image..."
	@docker build -t timebomb .

# Run with Docker Compose
docker-run:
	@echo "Starting TimeBomb with Docker Compose..."
	@docker-compose up -d

# Stop Docker Compose
docker-stop:
	@echo "Stopping TimeBomb..."
	@docker-compose down

# Run tests
test:
	@echo "Running tests..."
	@go test ./... -v

# Run linters
lint:
	@echo "Running linters..."
	@go vet ./...
	@go fmt ./...

# Install dependencies
deps:
	@echo "Installing dependencies..."
	@go mod download
	@go mod tidy

# Show help
help:
	@echo "Available targets:"
	@echo "  build        - Build the binary"
	@echo "  run          - Run the service locally"
	@echo "  clean        - Clean build artifacts"
	@echo "  docker-build - Build Docker image"
	@echo "  docker-run   - Run with Docker Compose"
	@echo "  docker-stop  - Stop Docker Compose"
	@echo "  test         - Run tests"
	@echo "  lint         - Run linters"
	@echo "  deps         - Install dependencies"
	@echo "  help         - Show this help message"

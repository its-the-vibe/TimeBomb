# Build stage
FROM golang:1.26rc3-alpine AS builder

# Install ca-certificates
RUN apk add --no-cache ca-certificates git

WORKDIR /build

# Copy go mod files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build the binary
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -ldflags '-extldflags "-static"' -o timebomb .

# Runtime stage
FROM scratch

# Copy CA certificates for HTTPS requests to Slack API
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Copy the binary
COPY --from=builder /build/timebomb /timebomb

# Run the binary
ENTRYPOINT ["/timebomb"]

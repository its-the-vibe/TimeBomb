# Build stage
FROM --platform=$BUILDPLATFORM golang:1.27.0-alpine AS builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /build

# Copy go mod files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build the binary
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -a -installsuffix cgo -ldflags '-extldflags "-static"' -o timebomb .

# Runtime stage
FROM gcr.io/distroless/static-debian13:nonroot

# Copy the binary
COPY --from=builder /build/timebomb /timebomb

USER nonroot:nonroot

# Run the binary
ENTRYPOINT ["/timebomb"]

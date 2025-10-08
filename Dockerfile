# Multi-stage build for high-traffic YouTube to MP3 API
FROM golang:1.24-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git ca-certificates tzdata

WORKDIR /app

# Copy go mod files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o ytmp3-api .

# Final stage
FROM alpine:latest

# Install runtime dependencies
RUN apk --no-cache add ca-certificates tzdata ffmpeg

# Install yt-dlp
RUN apk add --no-cache python3 py3-pip && \
    pip3 install yt-dlp && \
    ln -s /usr/bin/yt-dlp /usr/bin/yt-dlp

# Create app user
RUN addgroup -g 1001 -S appgroup && \
    adduser -u 1001 -S appuser -G appgroup

# Create necessary directories
RUN mkdir -p /app/downloads && \
    chown -R appuser:appgroup /app

WORKDIR /app

# Copy binary from builder stage
COPY --from=builder /app/ytmp3-api .

# Switch to non-root user
USER appuser

# Expose port
EXPOSE 8080

# Health check
HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://localhost:8080/health || exit 1

# Run the application
CMD ["./ytmp3-api"]
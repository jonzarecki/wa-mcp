# Multi-stage build for minimal final image with CGO support
FROM --platform=$BUILDPLATFORM golang:1.25-bookworm AS builder

ARG TARGETOS
ARG TARGETARCH

# Install build dependencies for CGO and SQLite (including cross-compilation tools)
RUN apt-get update && apt-get install -y \
    gcc \
    g++ \
    make \
    gcc-aarch64-linux-gnu \
    g++-aarch64-linux-gnu \
    gcc-x86-64-linux-gnu \
    g++-x86-64-linux-gnu \
    && rm -rf /var/lib/apt/lists/*

# Set working directory
WORKDIR /app

# Copy go module files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY cmd/ ./cmd/
COPY internal/ ./internal/

# Build the binary with CGO enabled and SQLite FTS5 support
# Configure cross-compilation toolchain based on target architecture
RUN if [ "$TARGETARCH" = "arm64" ]; then \
        export CC=aarch64-linux-gnu-gcc CXX=aarch64-linux-gnu-g++; \
    elif [ "$TARGETARCH" = "amd64" ]; then \
        export CC=x86_64-linux-gnu-gcc CXX=x86_64-linux-gnu-g++; \
    fi && \
    CGO_ENABLED=1 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -tags "sqlite_fts5" -o wa-mcp ./cmd/whatsapp-mcp

# Final stage - minimal runtime image
FROM --platform=$TARGETPLATFORM debian:bookworm-slim

# Install runtime dependencies (ffmpeg for audio conversion)
RUN apt-get update && apt-get install -y \
    ca-certificates \
    ffmpeg \
    && rm -rf /var/lib/apt/lists/*

# Set working directory
WORKDIR /app

# Copy binary from builder
COPY --from=builder /app/wa-mcp /app/wa-mcp

# Set environment variables
ENV DB_DIR=/app/store \
    LOG_LEVEL=INFO \
    FFMPEG_PATH=ffmpeg \
    MCP_TRANSPORT=stdio \
    MCP_HTTP_ADDR=:8085

# Create directory for database and media storage
RUN mkdir -p /app/store

# Expose HTTP port (used when MCP_TRANSPORT=http)
EXPOSE 8085

# Install netcat for healthcheck
RUN apt-get update && apt-get install -y netcat-openbsd && rm -rf /var/lib/apt/lists/*

HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
    CMD if [ "$MCP_TRANSPORT" = "http" ]; then nc -z localhost 8085; else test -f /app/wa-mcp; fi

ENTRYPOINT ["/app/wa-mcp"]

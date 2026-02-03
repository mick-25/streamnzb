# Build stage
FROM golang:1.25-alpine AS builder

WORKDIR /app

# Install build dependencies
RUN apk add --no-cache git

# Copy dependency files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code (including submodules if present on host)
COPY . .

# Build args set by buildx
ARG TARGETOS
ARG TARGETARCH

# Build the application
# Use TARGETOS and TARGETARCH to cross-compile for the target platform
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -ldflags="-s -w" -o streamnzb ./cmd/streamnzb

# Final stage
FROM alpine:latest

# Install runtime dependencies
RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

# Copy binary from builder
COPY --from=builder /app/streamnzb .

# Expose ports
# Addon port
EXPOSE 7000
# NNTP Proxy port (if enabled)
EXPOSE 1119

# Run the application
CMD ["./streamnzb"]

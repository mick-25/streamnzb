# Build stage
FROM golang:1.25-alpine AS builder

WORKDIR /app

# Install build dependencies
RUN apk add --no-cache git

# Copy dependency files
COPY go.mod go.sum ./
# Copy submodule go.mod and source for replacement
COPY pkg/external/sevenzip/ ./pkg/external/sevenzip/
RUN go mod download

# Copy source code
COPY . .

# Build args set by buildx or manual build
ARG TARGETOS
ARG TARGETARCH
ARG AVAILNZB_URL
ARG AVAILNZB_API_KEY

# Build the application
# We build for the current target platform by default
# But we also build other platforms for release extraction
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -ldflags="-s -w -X main.AvailNZBURL=${AVAILNZB_URL} -X main.AvailNZBAPIKey=${AVAILNZB_API_KEY}" -o streamnzb ./cmd/streamnzb


FROM alpine:latest

# Install runtime dependencies
RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

# Copy the native binary for the container
COPY --from=builder /app/streamnzb .

# Expose ports
EXPOSE 7000
EXPOSE 1119

# Run the application
CMD ["./streamnzb"]

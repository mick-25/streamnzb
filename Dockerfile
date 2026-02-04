ARG TARGETARCH

FROM alpine:latest

ARG TARGETARCH

# Install runtime dependencies
RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

# Copy the pre-built binary based on the target architecture
COPY dist/linux_${TARGETARCH}/streamnzb .

# Expose ports
EXPOSE 7000
EXPOSE 1119

# Run the application
CMD ["./streamnzb"]

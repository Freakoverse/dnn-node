# Build stage
FROM golang:1.21-alpine AS builder

# Install build dependencies
RUN apk add --no-cache gcc musl-dev sqlite-dev

# Set working directory
WORKDIR /app

# Copy go mod files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build the binary with SQLite support
RUN CGO_ENABLED=1 GOOS=linux go build -a -installsuffix cgo -o dnn-node .

# Runtime stage
FROM alpine:latest

# Install runtime dependencies
RUN apk --no-cache add ca-certificates sqlite-libs

# Create non-root user
RUN addgroup -g 1000 dnn && \
    adduser -u 1000 -G dnn -s /bin/sh -D dnn

# Set working directory
WORKDIR /home/dnn

# Copy binary from builder
COPY --from=builder /app/dnn-node .

# Create data directory
RUN mkdir -p data && chown -R dnn:dnn /home/dnn

# Switch to non-root user
USER dnn

# Expose default port
EXPOSE 8080

# Set default command
CMD ["./dnn-node"]
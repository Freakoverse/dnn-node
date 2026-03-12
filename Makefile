.PHONY: help build run test clean docker-build docker-run install deps fmt lint

# Default target
help:
	@echo "Available targets:"
	@echo "  make build       - Build the DNN node binary"
	@echo "  make run         - Run the DNN node"
	@echo "  make test        - Run tests"
	@echo "  make clean       - Clean build artifacts"
	@echo "  make docker-build - Build Docker image"
	@echo "  make docker-run  - Run with Docker Compose"
	@echo "  make install     - Install the binary globally"
	@echo "  make deps        - Download dependencies"
	@echo "  make fmt         - Format code"
	@echo "  make lint        - Run linter"

# Build the binary
build:
	go build -o dnn-node .

# Run the node
run: build
	./dnn-node

# Run with initialization
init: build
	./dnn-node --init

# Run tests
test:
	go test -v ./...

# Run tests with coverage
test-coverage:
	go test -v -cover ./...

# Clean build artifacts
clean:
	rm -f dnn-node
	rm -rf data/
	go clean

# Download dependencies
deps:
	go mod download
	go mod tidy

# Format code
fmt:
	go fmt ./...
	gofmt -s -w .

# Run linter (requires golangci-lint)
lint:
	@which golangci-lint > /dev/null || (echo "Installing golangci-lint..." && go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest)
	golangci-lint run

# Install binary globally
install:
	go install

# Build Docker image
docker-build:
	docker build -t godnn:latest .

# Run with Docker Compose
docker-run:
	docker-compose up -d

# Stop Docker Compose
docker-stop:
	docker-compose down

# View Docker logs
docker-logs:
	docker-compose logs -f

# Development mode with hot reload (requires air)
dev:
	@which air > /dev/null || (echo "Installing air..." && go install github.com/cosmtrek/air@latest)
	air

# Build for multiple platforms
build-all:
	GOOS=linux GOARCH=amd64 go build -o dist/dnn-node-linux-amd64
	GOOS=linux GOARCH=arm64 go build -o dist/dnn-node-linux-arm64
	GOOS=darwin GOARCH=amd64 go build -o dist/dnn-node-darwin-amd64
	GOOS=darwin GOARCH=arm64 go build -o dist/dnn-node-darwin-arm64
	GOOS=windows GOARCH=amd64 go build -o dist/dnn-node-windows-amd64.exe

# Create release archives
release: build-all
	cd dist && tar -czf dnn-node-linux-amd64.tar.gz dnn-node-linux-amd64
	cd dist && tar -czf dnn-node-linux-arm64.tar.gz dnn-node-linux-arm64
	cd dist && tar -czf dnn-node-darwin-amd64.tar.gz dnn-node-darwin-amd64
	cd dist && tar -czf dnn-node-darwin-arm64.tar.gz dnn-node-darwin-arm64
	cd dist && zip dnn-node-windows-amd64.zip dnn-node-windows-amd64.exe

# Database operations
db-init:
	sqlite3 data/dnn.db < schema.sql

db-backup:
	sqlite3 data/dnn.db ".backup data/dnn-backup-$(shell date +%Y%m%d-%H%M%S).db"

db-console:
	sqlite3 data/dnn.db

# Benchmarks
bench:
	go test -bench=. -benchmem ./...
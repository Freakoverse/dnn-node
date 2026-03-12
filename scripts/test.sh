#!/bin/bash

# DNN Node Test Runner

set -e

echo "DNN Node Test Suite"
echo "==================="
echo

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Check for required tools
check_command() {
    if ! command -v "$1" &> /dev/null; then
        echo -e "${RED}Error: $1 is required but not installed.${NC}"
        exit 1
    fi
}

echo "Checking dependencies..."
check_command go

# Run go fmt check
echo -e "\n${YELLOW}Running go fmt check...${NC}"
if [ -n "$(gofmt -l .)" ]; then
    echo -e "${RED}Code is not formatted. Please run: go fmt ./...${NC}"
    gofmt -l .
    exit 1
else
    echo -e "${GREEN}✓ Code formatting OK${NC}"
fi

# Run go vet
echo -e "\n${YELLOW}Running go vet...${NC}"
if go vet ./...; then
    echo -e "${GREEN}✓ go vet passed${NC}"
else
    echo -e "${RED}✗ go vet failed${NC}"
    exit 1
fi

# Run tests with coverage
echo -e "\n${YELLOW}Running tests with coverage...${NC}"
go test -v -cover -coverprofile=coverage.out ./...

# Generate coverage report
echo -e "\n${YELLOW}Generating coverage report...${NC}"
go tool cover -func=coverage.out

# Run benchmarks (optional)
if [ "$1" == "--bench" ]; then
    echo -e "\n${YELLOW}Running benchmarks...${NC}"
    go test -bench=. -benchmem ./...
fi

# Check for race conditions (optional)
if [ "$1" == "--race" ]; then
    echo -e "\n${YELLOW}Running tests with race detector...${NC}"
    go test -race ./...
fi

echo -e "\n${GREEN}All tests passed!${NC}"

# Display coverage summary
echo -e "\n${YELLOW}Coverage Summary:${NC}"
go tool cover -func=coverage.out | grep total

# Optional: Open coverage in browser
if [ "$1" == "--html" ]; then
    go tool cover -html=coverage.out -o coverage.html
    echo -e "\n${GREEN}Coverage report saved to coverage.html${NC}"
fi
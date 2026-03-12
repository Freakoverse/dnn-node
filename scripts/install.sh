#!/bin/bash

# DNN Node Installation Script

set -e

echo "DNN Node Installation Script"
echo "============================"
echo

# Detect OS and architecture
OS="$(uname -s)"
ARCH="$(uname -m)"

case "$OS" in
    Linux*)     PLATFORM="linux";;
    Darwin*)    PLATFORM="darwin";;
    CYGWIN*|MINGW*|MSYS*) PLATFORM="windows";;
    *)          echo "Unsupported OS: $OS"; exit 1;;
esac

case "$ARCH" in
    x86_64|amd64) ARCH="amd64";;
    aarch64|arm64) ARCH="arm64";;
    *) echo "Unsupported architecture: $ARCH"; exit 1;;
esac

echo "Detected platform: $PLATFORM-$ARCH"

# Check for required tools
check_command() {
    if ! command -v "$1" &> /dev/null; then
        echo "Error: $1 is required but not installed."
        exit 1
    fi
}

echo "Checking dependencies..."
check_command go
check_command git
check_command sqlite3

# Clone repository if not already in it
if [ ! -f "go.mod" ]; then
    echo "Cloning GoDNN repository..."
    git clone https://github.com/freakoverse/godnn.git
    cd godnn
fi

# Download Go dependencies
echo "Downloading Go dependencies..."
go mod download

# Build the binary
echo "Building DNN node..."
go build -o dnn-node .

# Create directories
echo "Creating data directory..."
mkdir -p data

# Initialize configuration
if [ ! -f "config.json" ]; then
    echo "Initializing configuration..."
    ./dnn-node --init
fi

echo ""
echo "Installation complete!"
echo ""
echo "To run the DNN node:"
echo "  ./dnn-node"
echo ""
echo "To install globally:"
echo "  sudo cp dnn-node /usr/local/bin/"
echo ""
echo "Configuration file: config.json"
echo "Data directory: ./data"
#!/bin/sh

# Graft Installation Script
# https://github.com/skssmd/graft

set -e

# Configuration
REPO="skssmd/Graft"
BINARY_NAME="graft"
INSTALL_PATH="/bin"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

printf "${BLUE}🚀 Starting Graft installation...${NC}\n"

# Detect OS and Architecture
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)

case $ARCH in
    x86_64) ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
    *) 
        printf "${RED}Unsupported architecture: $ARCH${NC}\n"
        exit 1 
        ;;
esac

if [ "$OS" != "linux" ]; then
    printf "${RED}This script only supports Linux.${NC}\n"
    exit 1
fi

# Determine download URL (using GitHub Releases as a standard target)
# NOTE: Replace this with your actual binary hosting URL if different
DOWNLOAD_URL="https://github.com/$REPO/releases/download/v1.0.0-linux/graft"

printf "${BLUE}🔍 Detected $OS ($ARCH). Fetching binary...${NC}\n"

# Download the binary
TMP_DIR=$(mktemp -d)
trap 'rm -rf "$TMP_DIR"' EXIT

if command -v curl >/dev/null 2>&1; then
    curl -L "$DOWNLOAD_URL" -o "$TMP_DIR/$BINARY_NAME"
elif command -v wget >/dev/null 2>&1; then
    wget -qO "$TMP_DIR/$BINARY_NAME" "$DOWNLOAD_URL"
else
    printf "${RED}Error: curl or wget is required to download Graft.${NC}\n"
    exit 1
fi

chmod +x "$TMP_DIR/$BINARY_NAME"

# Install to global path
printf "${BLUE}📦 Installing to $INSTALL_PATH/$BINARY_NAME...${NC}\n"

if [ -w "$INSTALL_PATH" ]; then
    mv "$TMP_DIR/$BINARY_NAME" "$INSTALL_PATH/$BINARY_NAME"
else
    printf "${BLUE}🔑 Sudo access required for installation path.${NC}\n"
    sudo mv "$TMP_DIR/$BINARY_NAME" "$INSTALL_PATH/$BINARY_NAME"
fi

printf "${GREEN}✨ Graft installed successfully!${NC}\n"
printf "Run ${BLUE}graft --help${NC} to get started.\n"

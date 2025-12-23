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

# Determine latest version if not provided
if [ -z "$VERSION" ]; then
    printf "${BLUE}🔍 Fetching latest version...${NC}\n"
    VERSION=$(curl -s "https://api.github.com/repos/$REPO/releases/latest" | grep '"tag_name":' | sed -E 's/.*"([^"]+)".*/\1/')
    if [ -z "$VERSION" ]; then
        printf "${RED}Error: Could not detect latest version.${NC}\n"
        exit 1
    fi
fi

# Determine download URL
# OS name needs to be capitalized for GoReleaser (Linux)
CAP_OS="Linux"
ARCHIVE_NAME="Graft_${VERSION#v}_${CAP_OS}_${ARCH}.tar.gz"
DOWNLOAD_URL="https://github.com/$REPO/releases/download/$VERSION/$ARCHIVE_NAME"

printf "${BLUE}🔍 Detected $OS ($ARCH), Version $VERSION. Fetching $ARCHIVE_NAME...${NC}\n"

# Download and extract the binary
TMP_DIR=$(mktemp -d)
trap 'rm -rf "$TMP_DIR"' EXIT

if command -v curl >/dev/null 2>&1; then
    curl -L "$DOWNLOAD_URL" -o "$TMP_DIR/$ARCHIVE_NAME"
elif command -v wget >/dev/null 2>&1; then
    wget -qO "$TMP_DIR/$ARCHIVE_NAME" "$DOWNLOAD_URL"
else
    printf "${RED}Error: curl or wget is required.${NC}\n"
    exit 1
fi

tar -xzf "$TMP_DIR/$ARCHIVE_NAME" -C "$TMP_DIR"
chmod +x "$TMP_DIR/$BINARY_NAME"

# Install to global path
printf "${BLUE}📦 Installing to $INSTALL_PATH/$BINARY_NAME...${NC}\n"

if [ -w "$INSTALL_PATH" ]; then
    mv "$TMP_DIR/$BINARY_NAME" "$INSTALL_PATH/$BINARY_NAME"
else
    printf "${BLUE}🔑 Sudo access required for installation path.${NC}\n"
    sudo mv "$TMP_DIR/$BINARY_NAME" "$INSTALL_PATH/$BINARY_NAME"
fi

printf "${GREEN}✨ Graft $VERSION installed successfully!${NC}\n"
printf "Run ${BLUE}graft --help${NC} to get started.\n"

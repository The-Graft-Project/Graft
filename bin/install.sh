#!/bin/sh

# Graft Installation Script
# https://github.com/skssmd/graft

set -e

# Configuration
REPO="skssmd/Graft"
BINARY_NAME="graft"
INSTALL_PATH="/usr/local/bin"  # ⚠️ Changed from /bin - see note below

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
BLUE='\033[0;34m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

printf "${BLUE}🚀 Starting Graft installation...${NC}\n"

# Detect OS and Architecture
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)

case $ARCH in
    x86_64) ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
    armv7l) ARCH="armv7" ;;  # Added for Raspberry Pi support
    *) 
        printf "${RED}❌ Unsupported architecture: $ARCH${NC}\n"
        exit 1 
        ;;
esac

# Support both Linux and macOS
case $OS in
    linux) CAP_OS="Linux" ;;
    darwin) CAP_OS="Darwin" ;;
    *)
        printf "${RED}❌ Unsupported OS: $OS${NC}\n"
        printf "${YELLOW}This script supports Linux and macOS only.${NC}\n"
        exit 1
        ;;
esac

# Determine latest version if not provided
if [ -z "$VERSION" ]; then
    printf "${BLUE}🔍 Fetching latest version...${NC}\n"
    VERSION=$(curl -sL "https://api.github.com/repos/$REPO/releases/latest" | grep '"tag_name":' | sed -E 's/.*"([^"]+)".*/\1/')
    if [ -z "$VERSION" ]; then
        printf "${RED}❌ Error: Could not detect latest version.${NC}\n"
        printf "${YELLOW}Check your internet connection or specify VERSION manually.${NC}\n"
        exit 1
    fi
fi

# Construct download URL
ARCHIVE_NAME="Graft_${VERSION#v}_${CAP_OS}_${ARCH}.tar.gz"
DOWNLOAD_URL="https://github.com/$REPO/releases/download/$VERSION/$ARCHIVE_NAME"

printf "${BLUE}📦 Downloading Graft $VERSION for $OS ($ARCH)...${NC}\n"

# Create temporary directory
TMP_DIR=$(mktemp -d)
trap 'rm -rf "$TMP_DIR"' EXIT INT TERM

# Download with progress and error handling
if command -v curl >/dev/null 2>&1; then
    if ! curl -fL --progress-bar "$DOWNLOAD_URL" -o "$TMP_DIR/$ARCHIVE_NAME"; then
        printf "${RED}❌ Download failed. Please check:${NC}\n"
        printf "   • Internet connection\n"
        printf "   • Release exists: $DOWNLOAD_URL\n"
        exit 1
    fi
elif command -v wget >/dev/null 2>&1; then
    if ! wget --show-progress -qO "$TMP_DIR/$ARCHIVE_NAME" "$DOWNLOAD_URL"; then
        printf "${RED}❌ Download failed.${NC}\n"
        exit 1
    fi
else
    printf "${RED}❌ Error: curl or wget is required.${NC}\n"
    exit 1
fi

# Extract and verify
printf "${BLUE}📂 Extracting...${NC}\n"
tar -xzf "$TMP_DIR/$ARCHIVE_NAME" -C "$TMP_DIR"

if [ ! -f "$TMP_DIR/$BINARY_NAME" ]; then
    printf "${RED}❌ Error: Binary not found in archive.${NC}\n"
    exit 1
fi

chmod +x "$TMP_DIR/$BINARY_NAME"

# Install to global path
printf "${BLUE}🔧 Installing to $INSTALL_PATH/$BINARY_NAME...${NC}\n"

if [ -w "$INSTALL_PATH" ]; then
    mv "$TMP_DIR/$BINARY_NAME" "$INSTALL_PATH/$BINARY_NAME"
else
    printf "${YELLOW}🔑 Requesting sudo access for installation...${NC}\n"
    sudo mv "$TMP_DIR/$BINARY_NAME" "$INSTALL_PATH/$BINARY_NAME"
fi

# Verify installation
if command -v graft >/dev/null 2>&1; then
    INSTALLED_VERSION=$(graft --version 2>/dev/null || echo "unknown")
    printf "${GREEN}✨ Graft $VERSION installed successfully!${NC}\n"
    printf "${BLUE}📍 Location: $INSTALL_PATH/$BINARY_NAME${NC}\n"
    printf "\nRun ${GREEN}graft --help${NC} to get started.\n"
else
    printf "${YELLOW}⚠️  Installation complete but 'graft' not found in PATH.${NC}\n"
    printf "Add $INSTALL_PATH to your PATH or run: $INSTALL_PATH/$BINARY_NAME\n"
fi
#!/usr/bin/env bash
set -euo pipefail

# Telara CLI installer
# Usage: curl -fsSL https://get.telara.dev/install.sh | sh

REPO="Telara-Labs/Telara-CLI"
BINARY="telara"
INSTALL_DIR="${TELARA_INSTALL_DIR:-/usr/local/bin}"

PRIMARY_BASE_URL="https://get.telara.dev"
GITHUB_API_URL="https://api.github.com/repos/${REPO}/releases/latest"
GITHUB_DOWNLOAD_URL="https://github.com/${REPO}/releases/download"

# Detect OS and arch
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"

case "$ARCH" in
  x86_64)  ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *)
    echo "Unsupported architecture: $ARCH" >&2
    exit 1
    ;;
esac

case "$OS" in
  linux|darwin) ;;
  *)
    echo "Unsupported OS: $OS" >&2
    echo "For Windows, run: irm https://get.telara.dev/windows | iex" >&2
    exit 1
    ;;
esac

# Get latest version
VERSION="${TELARA_VERSION:-}"
if [ -z "$VERSION" ]; then
  # Try primary CDN first (check non-empty response), fall back to GitHub Releases API
  VERSION="$(curl -fsSL "${PRIMARY_BASE_URL}/latest-version" 2>/dev/null)"
  if [ -z "$VERSION" ]; then
    echo "Primary version endpoint unavailable or empty, trying GitHub Releases..." >&2
    VERSION="$(curl -fsSL "${GITHUB_API_URL}" | grep '"tag_name"' | sed -E 's/.*"tag_name":\s*"([^"]+)".*/\1/')"
  fi
fi

# Strip v prefix for filename (GoReleaser uses version without v)
VERSION_NUM="${VERSION#v}"

echo "Installing telara ${VERSION} (${OS}/${ARCH})..."

# Download URL
FILENAME="${BINARY}_${VERSION_NUM}_${OS}_${ARCH}.tar.gz"

# Ensure tag has v prefix for GitHub Releases URL
TAG="${VERSION}"
case "$TAG" in
  v*) ;;
  *)  TAG="v${TAG}" ;;
esac

PRIMARY_URL="${PRIMARY_BASE_URL}/download/${VERSION}/${FILENAME}"
FALLBACK_URL="${GITHUB_DOWNLOAD_URL}/${TAG}/${FILENAME}"

# Download and extract
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

if ! curl -fsSL "$PRIMARY_URL" -o "$TMP/$FILENAME" 2>/dev/null || ! gzip -t "$TMP/$FILENAME" 2>/dev/null; then
  echo "Primary download unavailable or invalid, trying GitHub Releases..." >&2
  curl -fsSL "$FALLBACK_URL" -o "$TMP/$FILENAME"
fi

tar -xzf "$TMP/$FILENAME" -C "$TMP"

# Install
if [ -w "$INSTALL_DIR" ]; then
  mv "$TMP/$BINARY" "$INSTALL_DIR/$BINARY"
else
  echo "Installing to $INSTALL_DIR requires sudo..."
  sudo mv "$TMP/$BINARY" "$INSTALL_DIR/$BINARY"
fi

chmod +x "$INSTALL_DIR/$BINARY"

echo ""
echo "telara installed to $INSTALL_DIR/$BINARY"
echo ""
echo "Get started:"
echo "  1. Generate a token at https://app.telara.dev/settings?tab=developer"
echo "  2. telara login --token <your-token>"
echo "  3. telara setup claude-code"

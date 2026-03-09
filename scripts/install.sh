#!/usr/bin/env bash
set -euo pipefail

# Telara CLI installer
# Usage: curl -fsSL https://get.telara.ai/install.sh | sh

REPO="telera-ai/telera-cli"
BINARY="telara"
INSTALL_DIR="${TELARA_INSTALL_DIR:-/usr/local/bin}"

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
    echo "For Windows, run: irm https://get.telara.ai/windows | iex" >&2
    exit 1
    ;;
esac

# Get latest version
VERSION="${TELARA_VERSION:-}"
if [ -z "$VERSION" ]; then
  VERSION="$(curl -fsSL "https://get.telara.ai/latest-version")"
fi

echo "Installing telara ${VERSION} (${OS}/${ARCH})..."

# Download URL
FILENAME="${BINARY}_${VERSION#v}_${OS}_${ARCH}.tar.gz"
URL="https://get.telara.ai/download/${VERSION}/${FILENAME}"

# Download and extract
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

curl -fsSL "$URL" -o "$TMP/$FILENAME"
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
echo "  1. Generate a token at https://app.telara.ai/settings?tab=developer"
echo "  2. telara login --token <your-token>"
echo "  3. telara setup claude-code"

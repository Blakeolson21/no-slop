#!/bin/sh
set -e

REPO="Blakeolson21/no-slop"
resolve_env_alias() {
  canonical="$1"
  legacy="$2"
  eval "canonical_set=\${$canonical+x}"
  eval "legacy_set=\${$legacy+x}"
  eval "canonical_value=\${$canonical-}"
  eval "legacy_value=\${$legacy-}"
  if [ -n "$canonical_set" ] && [ -n "$legacy_set" ] && [ "$canonical_value" != "$legacy_value" ]; then
    echo "$canonical and $legacy configure the same setting with different values" >&2
    exit 2
  fi
  if [ -n "$canonical_set" ]; then
    printf '%s' "$canonical_value"
    return
  fi
  if [ -n "$legacy_set" ]; then
    printf '%s' "$legacy_value"
  fi
}

INSTALL_DIR="$(resolve_env_alias NS_INSTALL_DIR NO_MISTAKES_INSTALL_DIR)"
INSTALL_DIR="${INSTALL_DIR:-$HOME/.no-mistakes/bin}"
LINK_DIR="$(resolve_env_alias NS_LINK_DIR NO_MISTAKES_LINK_DIR)"

if [ -z "$LINK_DIR" ]; then
  case ":$PATH:" in
    *":$HOME/.local/bin:"*) LINK_DIR="$HOME/.local/bin" ;;
    *) LINK_DIR="/usr/local/bin" ;;
  esac
fi

BIN_PATH="$INSTALL_DIR/no-slop"
LEGACY_BIN_PATH="$INSTALL_DIR/no-mistakes"
LINK_PATH="$LINK_DIR/no-slop"
LEGACY_LINK_PATH="$LINK_DIR/no-mistakes"

OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"

case "$OS" in
  darwin|linux) ;;
  *) echo "Unsupported OS: $OS"; exit 1 ;;
esac

case "$ARCH" in
  x86_64|amd64) ARCH="amd64" ;;
  arm64|aarch64) ARCH="arm64" ;;
  *) echo "Unsupported architecture: $ARCH"; exit 1 ;;
esac

VERSION="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1)"
if [ -z "$VERSION" ]; then
  echo "Could not determine latest release"
  exit 1
fi

FILENAME="no-slop-${VERSION}-${OS}-${ARCH}.tar.gz"
URL="https://github.com/${REPO}/releases/download/${VERSION}/${FILENAME}"

TMPDIR="$(mktemp -d)"
trap 'rm -rf "$TMPDIR"' EXIT

echo "Downloading no-slop ${VERSION} for ${OS}/${ARCH}..."
curl -fsSL "$URL" -o "${TMPDIR}/${FILENAME}"
tar xzf "${TMPDIR}/${FILENAME}" -C "$TMPDIR"

if ! mkdir -p "$INSTALL_DIR"; then
  echo "Could not create install directory: $INSTALL_DIR"
  exit 1
fi

mv "${TMPDIR}/no-slop" "$BIN_PATH"
chmod 755 "$BIN_PATH" 2>/dev/null || true
rm -f "$LEGACY_BIN_PATH"
ln -s "$BIN_PATH" "$LEGACY_BIN_PATH"

resolve_path() {
  (cd "$1" 2>/dev/null && pwd -P)
}

REAL_INSTALL_DIR="$(resolve_path "$INSTALL_DIR")"
REAL_LINK_DIR="$(resolve_path "$LINK_DIR" 2>/dev/null || echo "")"

if [ -n "$REAL_INSTALL_DIR" ] && [ "$REAL_INSTALL_DIR" = "$REAL_LINK_DIR" ]; then
  rm -f "$LEGACY_LINK_PATH"
  ln -s "$BIN_PATH" "$LEGACY_LINK_PATH"
  echo "Install dir and link dir resolve to the same path; canonical command is already installed."
else
  if [ -w "$LINK_DIR" ] || (mkdir -p "$LINK_DIR" 2>/dev/null && [ -w "$LINK_DIR" ]); then
    rm -f "$LINK_PATH"
    ln -s "$BIN_PATH" "$LINK_PATH"
    rm -f "$LEGACY_LINK_PATH"
    ln -s "$BIN_PATH" "$LEGACY_LINK_PATH"
  else
    echo "Linking ${LINK_PATH} to ${BIN_PATH} (requires sudo)..."
    sudo mkdir -p "$LINK_DIR"
    sudo rm -f "$LINK_PATH"
    sudo ln -s "$BIN_PATH" "$LINK_PATH"
    sudo rm -f "$LEGACY_LINK_PATH"
    sudo ln -s "$BIN_PATH" "$LEGACY_LINK_PATH"
  fi
fi

echo "no-slop ${VERSION} installed to ${BIN_PATH}"
echo "Command path: ${LINK_PATH} -> ${BIN_PATH}"
echo "Compatibility alias: ${LEGACY_LINK_PATH} -> ${BIN_PATH}"

"$BIN_PATH" daemon restart >/dev/null

case ":$PATH:" in
  *":$LINK_DIR:"*) ;;
  *) echo "Add ${LINK_DIR} to your PATH and restart your terminal." ;;
esac

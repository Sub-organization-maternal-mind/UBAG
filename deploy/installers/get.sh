#!/usr/bin/env bash
# get.sh — UBAG one-line installer
#
# Downloads the correct pre-built release archive from GitHub, verifies its
# sha256 checksum BEFORE extracting, then installs the `ubag` binary.
#
# Usage:
#   curl -fsSL https://get.ubag.dev | sh
#   curl -fsSL https://get.ubag.dev | sh -s -- --prefix /usr/local
#   UBAG_VERSION=v1.2.3 curl -fsSL https://get.ubag.dev | sh
#
# Environment variables:
#   UBAG_VERSION   Specific release tag to install (default: latest)
#
# Options:
#   --prefix <dir>   Install prefix (default: $HOME/.local for non-root,
#                    /usr/local for root)
#
set -euo pipefail

UBAG_VERSION="${UBAG_VERSION:-latest}"
INSTALL_PREFIX=""
REPO="ubag/ubag"
BASE_URL="https://github.com/${REPO}/releases"

log()  { printf '[ubag] %s\n' "$*"; }
err()  { printf '[ubag] ERROR: %s\n' "$*" >&2; exit 1; }

# Parse --prefix argument (support both "| sh -s -- --prefix /..." and direct)
while [ $# -gt 0 ]; do
  case "$1" in
    --prefix) INSTALL_PREFIX="${2:?--prefix requires a value}"; shift 2 ;;
    *) err "unknown argument: $1" ;;
  esac
done

# ---------------------------------------------------------------------------
# Platform detection
# ---------------------------------------------------------------------------
detect_platform() {
  OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
  ARCH="$(uname -m)"
  case "$ARCH" in
    x86_64)        ARCH="amd64" ;;
    aarch64|arm64) ARCH="arm64" ;;
    *) err "Unsupported architecture: $ARCH" ;;
  esac
  case "$OS" in
    linux|darwin) ;;
    *) err "Unsupported OS: $OS" ;;
  esac
}

# ---------------------------------------------------------------------------
# Download helper (curl or wget)
# ---------------------------------------------------------------------------
download() {
  local url="$1" dest="$2"
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$url" -o "$dest"
  elif command -v wget >/dev/null 2>&1; then
    wget -q "$url" -O "$dest"
  else
    err "curl or wget is required but neither was found"
  fi
}

# Download to stdout (used for version resolution)
download_stdout() {
  local url="$1"
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$url"
  elif command -v wget >/dev/null 2>&1; then
    wget -q "$url" -O -
  else
    err "curl or wget is required but neither was found"
  fi
}

# ---------------------------------------------------------------------------
# sha256 verification — MUST be called BEFORE extraction
# ---------------------------------------------------------------------------
verify_checksum() {
  local archive="$1" checksums="$2"
  local filename actual expected

  filename="$(basename "$archive")"
  expected="$(grep "$filename" "$checksums" | awk '{print $1}')"

  if [ -z "$expected" ]; then
    err "checksum not found in checksums.txt for: $filename"
  fi

  if command -v sha256sum >/dev/null 2>&1; then
    actual="$(sha256sum "$archive" | awk '{print $1}')"
  elif command -v shasum >/dev/null 2>&1; then
    actual="$(shasum -a 256 "$archive" | awk '{print $1}')"
  else
    err "sha256sum or shasum is required for checksum verification"
  fi

  if [ "$actual" != "$expected" ]; then
    printf '[ubag] ERROR: sha256 mismatch for %s\n' "$filename" >&2
    printf '[ubag]   Expected: %s\n' "$expected" >&2
    printf '[ubag]   Got:      %s\n' "$actual" >&2
    exit 1
  fi

  log "sha256 verified: $filename"
}

# ---------------------------------------------------------------------------
# Resolve latest version tag from GitHub API
# ---------------------------------------------------------------------------
resolve_version() {
  local tag
  tag="$(download_stdout "https://api.github.com/repos/${REPO}/releases/latest" \
        | grep '"tag_name"' \
        | head -1 \
        | cut -d'"' -f4)"
  [ -n "$tag" ] || err "could not determine latest release version from GitHub API"
  echo "$tag"
}

# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------
main() {
  detect_platform

  # Resolve version
  if [ "$UBAG_VERSION" = "latest" ]; then
    log "resolving latest release..."
    UBAG_VERSION="$(resolve_version)"
  fi

  log "installing ubag ${UBAG_VERSION} (${OS}/${ARCH})"

  local archive_name="ubag_${UBAG_VERSION}_${OS}_${ARCH}.tar.gz"
  local download_url="${BASE_URL}/download/${UBAG_VERSION}/${archive_name}"
  local checksums_url="${BASE_URL}/download/${UBAG_VERSION}/checksums.txt"

  # Temp workspace — always cleaned up
  local tmpdir
  tmpdir="$(mktemp -d)"
  trap 'rm -rf "$tmpdir"' EXIT

  # Download archive and checksums
  log "downloading ${archive_name}..."
  download "$download_url" "${tmpdir}/${archive_name}"

  log "downloading checksums.txt..."
  download "$checksums_url" "${tmpdir}/checksums.txt"

  # VERIFY CHECKSUM BEFORE EXTRACTION (critical safety step)
  verify_checksum "${tmpdir}/${archive_name}" "${tmpdir}/checksums.txt"

  # Extract only after a clean verification
  log "extracting archive..."
  tar -xzf "${tmpdir}/${archive_name}" -C "$tmpdir"

  # Determine install prefix
  if [ -z "$INSTALL_PREFIX" ]; then
    if [ "$(id -u)" = "0" ]; then
      INSTALL_PREFIX="/usr/local"
    else
      INSTALL_PREFIX="${HOME}/.local"
    fi
  fi

  # Install binary
  mkdir -p "${INSTALL_PREFIX}/bin"
  install -m 0755 "${tmpdir}/ubag" "${INSTALL_PREFIX}/bin/ubag"
  log "installed: ${INSTALL_PREFIX}/bin/ubag"

  # Warn if install prefix is not on PATH
  case ":${PATH}:" in
    *":${INSTALL_PREFIX}/bin:"*) ;;
    *) log "NOTE: add '${INSTALL_PREFIX}/bin' to your PATH to use ubag" ;;
  esac

  # Run init
  if command -v "${INSTALL_PREFIX}/bin/ubag" >/dev/null 2>&1; then
    "${INSTALL_PREFIX}/bin/ubag" init 2>/dev/null || true
  fi

  log "done — run 'ubag --help' to get started."
}

main "$@"

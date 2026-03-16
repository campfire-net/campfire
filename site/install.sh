#!/bin/sh
# Campfire install script
# Usage: curl -fsSL https://getcampfire.dev/install.sh | sh
#
# Installs cf and cf-mcp to ~/.local/bin

set -e

REPO="campfire-net/campfire"
INSTALL_DIR="${HOME}/.local/bin"

# Colors (only if terminal supports them)
if [ -t 1 ]; then
  RED='\033[0;31m'
  GREEN='\033[0;32m'
  YELLOW='\033[1;33m'
  BOLD='\033[1m'
  RESET='\033[0m'
else
  RED=''
  GREEN=''
  YELLOW=''
  BOLD=''
  RESET=''
fi

info()    { printf "${BOLD}%s${RESET}\n" "$1"; }
success() { printf "${GREEN}%s${RESET}\n" "$1"; }
warn()    { printf "${YELLOW}%s${RESET}\n" "$1" >&2; }
die()     { printf "${RED}error: %s${RESET}\n" "$1" >&2; exit 1; }

# Detect OS
detect_os() {
  case "$(uname -s)" in
    Linux*)  echo "linux" ;;
    Darwin*) echo "darwin" ;;
    *)       die "Unsupported OS: $(uname -s). Download manually from https://github.com/${REPO}/releases" ;;
  esac
}

# Detect architecture
detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64) echo "amd64" ;;
    aarch64|arm64) echo "arm64" ;;
    *) die "Unsupported architecture: $(uname -m). Download manually from https://github.com/${REPO}/releases" ;;
  esac
}

# Check for required tools
check_deps() {
  for cmd in curl tar sha256sum; do
    if ! command -v "$cmd" >/dev/null 2>&1; then
      # macOS ships shasum, not sha256sum
      if [ "$cmd" = "sha256sum" ] && command -v shasum >/dev/null 2>&1; then
        continue
      fi
      die "Required tool not found: $cmd"
    fi
  done
}

# Compute sha256 of a file (handles macOS vs Linux)
sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

# Get the latest release tag from GitHub API
get_latest_version() {
  local url="https://api.github.com/repos/${REPO}/releases/latest"
  local version

  if command -v curl >/dev/null 2>&1; then
    version=$(curl -fsSL "$url" | grep '"tag_name"' | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/')
  fi

  if [ -z "$version" ]; then
    die "Could not determine latest version. Check your internet connection or visit https://github.com/${REPO}/releases"
  fi

  echo "$version"
}

main() {
  info "Campfire installer"
  printf "\n"

  check_deps

  OS=$(detect_os)
  ARCH=$(detect_arch)

  info "Detecting platform..."
  printf "  OS:   %s\n" "$OS"
  printf "  Arch: %s\n" "$ARCH"
  printf "\n"

  info "Finding latest release..."
  VERSION=$(get_latest_version)
  printf "  Version: %s\n" "$VERSION"
  printf "\n"

  # Strip leading 'v' from version for archive name if present
  # Archive naming from release.yml: cf_linux_amd64.tar.gz
  LABEL="${OS}_${ARCH}"
  ARCHIVE="cf_${LABEL}.tar.gz"
  BASE_URL="https://github.com/${REPO}/releases/download/${VERSION}"
  ARCHIVE_URL="${BASE_URL}/${ARCHIVE}"
  CHECKSUMS_URL="${BASE_URL}/checksums.txt"

  # Create temp directory
  TMP_DIR=$(mktemp -d)
  trap 'rm -rf "$TMP_DIR"' EXIT

  info "Downloading..."
  printf "  %s\n" "$ARCHIVE_URL"

  if ! curl -fsSL --progress-bar -o "${TMP_DIR}/${ARCHIVE}" "$ARCHIVE_URL"; then
    die "Download failed. Check that version ${VERSION} has a release for ${LABEL}.\nVisit https://github.com/${REPO}/releases"
  fi

  # Download checksums
  printf "  checksums.txt\n"
  if ! curl -fsSL -o "${TMP_DIR}/checksums.txt" "$CHECKSUMS_URL"; then
    warn "Could not download checksums — skipping verification"
  else
    printf "\n"
    info "Verifying checksum..."
    EXPECTED=$(grep "${ARCHIVE}" "${TMP_DIR}/checksums.txt" | awk '{print $1}')
    if [ -z "$EXPECTED" ]; then
      warn "Checksum entry not found for ${ARCHIVE} — skipping verification"
    else
      ACTUAL=$(sha256_file "${TMP_DIR}/${ARCHIVE}")
      if [ "$ACTUAL" != "$EXPECTED" ]; then
        die "Checksum mismatch!\n  expected: ${EXPECTED}\n  got:      ${ACTUAL}"
      fi
      success "  Checksum OK"
    fi
  fi

  printf "\n"
  info "Extracting..."

  tar -xzf "${TMP_DIR}/${ARCHIVE}" -C "${TMP_DIR}"

  # Binaries land in cf_${LABEL}/ and cf-mcp_${LABEL}/ inside the archive
  CF_BIN="${TMP_DIR}/cf_${LABEL}/cf"
  CFMCP_BIN="${TMP_DIR}/cf-mcp_${LABEL}/cf-mcp"

  if [ ! -f "$CF_BIN" ]; then
    die "cf binary not found in archive. Unexpected archive layout."
  fi
  if [ ! -f "$CFMCP_BIN" ]; then
    die "cf-mcp binary not found in archive. Unexpected archive layout."
  fi

  # Install
  printf "\n"
  info "Installing to ${INSTALL_DIR}..."
  mkdir -p "$INSTALL_DIR"

  cp "$CF_BIN" "${INSTALL_DIR}/cf"
  cp "$CFMCP_BIN" "${INSTALL_DIR}/cf-mcp"
  chmod +x "${INSTALL_DIR}/cf" "${INSTALL_DIR}/cf-mcp"

  success "  cf     → ${INSTALL_DIR}/cf"
  success "  cf-mcp → ${INSTALL_DIR}/cf-mcp"

  # PATH advice
  printf "\n"
  case ":${PATH}:" in
    *":${INSTALL_DIR}:"*)
      success "Done! ${INSTALL_DIR} is already in your PATH."
      ;;
    *)
      warn "${INSTALL_DIR} is not in your PATH."
      printf "\nAdd it by running:\n\n"
      printf "  ${BOLD}echo 'export PATH=\"\$HOME/.local/bin:\$PATH\"' >> ~/.profile${RESET}\n"
      printf "  ${BOLD}source ~/.profile${RESET}\n"
      printf "\nOr for zsh:\n\n"
      printf "  ${BOLD}echo 'export PATH=\"\$HOME/.local/bin:\$PATH\"' >> ~/.zshrc${RESET}\n"
      printf "  ${BOLD}source ~/.zshrc${RESET}\n"
      ;;
  esac

  printf "\n"
  info "Next steps:"
  printf "\n"
  printf "  cf init                        # generate your identity\n"
  printf "  cf create --protocol open      # create a campfire\n"
  printf "  cf discover                    # find nearby campfires\n"
  printf "\n"
  printf "  Docs: https://getcampfire.dev/docs/getting-started.html\n"
  printf "\n"
}

main "$@"

#!/bin/sh
# publish.sh — extract cf-mcp binaries from dist/, bump package versions, publish to npm
#
# Usage (called from release workflow):
#   NPM_TOKEN=... RELEASE_TAG=v0.1.2 ./npm/publish.sh
#
# Prerequisites:
#   - dist/ contains the release archives built by goreleaser
#   - NPM_TOKEN env var set (npm auth)
#   - RELEASE_TAG env var set (e.g. "v0.1.2")

set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
DIST_DIR="$(cd "$SCRIPT_DIR/.." && pwd)/dist"

if [ -z "$RELEASE_TAG" ]; then
  echo "ERROR: RELEASE_TAG is not set (e.g. v0.1.2)" >&2
  exit 1
fi

if [ -z "$NPM_TOKEN" ]; then
  echo "ERROR: NPM_TOKEN is not set" >&2
  exit 1
fi

# Strip leading 'v' to get npm version (v0.1.2 -> 0.1.2)
NPM_VERSION="${RELEASE_TAG#v}"

echo "Publishing campfire-mcp@${NPM_VERSION} to npm"

# Map: npm package name -> dist archive name (without .tar.gz)
# Archive names match goreleaser output: cf_linux_amd64.tar.gz, etc.
extract_binary() {
  pkg_name="$1"   # e.g. campfire-mcp-linux-x64
  archive="$2"    # e.g. cf_linux_amd64
  bin_name="${3:-cf-mcp}"  # binary name inside archive (default: cf-mcp)

  pkg_dir="$SCRIPT_DIR/$pkg_name"
  archive_path="$DIST_DIR/${archive}.tar.gz"

  if [ ! -f "$archive_path" ]; then
    echo "WARNING: $archive_path not found, skipping $pkg_name" >&2
    return
  fi

  echo "Extracting $bin_name from $archive.tar.gz -> $pkg_dir/"
  tar -xzf "$archive_path" -C "$pkg_dir" "$bin_name"
  chmod +x "$pkg_dir/$bin_name"
}

extract_win_binary() {
  pkg_name="$1"
  archive="$2"
  bin_name="${3:-cf-mcp.exe}"

  pkg_dir="$SCRIPT_DIR/$pkg_name"
  archive_path="$DIST_DIR/${archive}.zip"

  if [ ! -f "$archive_path" ]; then
    echo "WARNING: $archive_path not found, skipping $pkg_name" >&2
    return
  fi

  echo "Extracting $bin_name from $archive.zip -> $pkg_dir/"
  unzip -o "$archive_path" "$bin_name" -d "$pkg_dir"
  chmod +x "$pkg_dir/$bin_name"
}

# Extract platform binaries
extract_binary    "campfire-mcp-linux-x64"    "cf_linux_amd64"   "cf-mcp"
extract_binary    "campfire-mcp-linux-arm64"  "cf_linux_arm64"   "cf-mcp"
extract_binary    "campfire-mcp-darwin-x64"   "cf_darwin_amd64"  "cf-mcp"
extract_binary    "campfire-mcp-darwin-arm64" "cf_darwin_arm64"  "cf-mcp"
extract_win_binary "campfire-mcp-win32-x64"   "cf_windows_amd64" "cf-mcp.exe"

# Bump version in all package.json files
bump_version() {
  pkg_dir="$SCRIPT_DIR/$1"
  pkg_json="$pkg_dir/package.json"
  # Use node to update version (available wherever npm is)
  node -e "
    const fs = require('fs');
    const pkg = JSON.parse(fs.readFileSync('$pkg_json', 'utf8'));
    pkg.version = '$NPM_VERSION';
    // Update optionalDependencies versions if present
    if (pkg.optionalDependencies) {
      for (const k of Object.keys(pkg.optionalDependencies)) {
        pkg.optionalDependencies[k] = '$NPM_VERSION';
      }
    }
    fs.writeFileSync('$pkg_json', JSON.stringify(pkg, null, 2) + '\n');
    console.log('Bumped', '$1', 'to', '$NPM_VERSION');
  "
}

for pkg in \
  campfire-mcp-linux-x64 \
  campfire-mcp-linux-arm64 \
  campfire-mcp-darwin-x64 \
  campfire-mcp-darwin-arm64 \
  campfire-mcp-win32-x64 \
  campfire-mcp; do
  bump_version "$pkg"
done

# Configure npm auth
echo "//registry.npmjs.org/:_authToken=${NPM_TOKEN}" > "$HOME/.npmrc"

# Publish platform packages first (main package depends on them)
for pkg in \
  campfire-mcp-linux-x64 \
  campfire-mcp-linux-arm64 \
  campfire-mcp-darwin-x64 \
  campfire-mcp-darwin-arm64 \
  campfire-mcp-win32-x64; do
  echo "Publishing $pkg..."
  (cd "$SCRIPT_DIR/$pkg" && npm publish --access public) || echo "WARNING: $pkg publish failed (may already exist)"
done

# Publish main package
echo "Publishing campfire-mcp..."
(cd "$SCRIPT_DIR/campfire-mcp" && npm publish --access public)

echo "Done. campfire-mcp@${NPM_VERSION} published."

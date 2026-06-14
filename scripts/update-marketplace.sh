#!/bin/bash
# Stamp marketplace.json with the checksums from a goreleaser release.
# Called by the release pipeline after artifacts are published.
#
# Usage: update-marketplace.sh <version> [checksums_file]
#   version       — semver string without the leading "v" (e.g. "1.2.3")
#   checksums_file — path to the goreleaser checksums.txt (default: dist/checksums.txt)

set -e

VERSION="${1:-}"
CHECKSUMS_FILE="${2:-dist/checksums.txt}"

if [[ -z "$VERSION" ]]; then
    echo "Usage: $0 <version> [checksums_file]"
    exit 1
fi

if [[ ! -f "$CHECKSUMS_FILE" ]]; then
    echo "Checksums file not found: $CHECKSUMS_FILE"
    exit 1
fi

VERSION_TAG="v${VERSION}"
RELEASE_DATE=$(date -u +"%Y-%m-%d")

# Extract per-platform checksums from the goreleaser checksums file.
SHA_DARWIN_AMD64=$(grep "darwin_amd64" "$CHECKSUMS_FILE" | awk '{print $1}' || echo "")
SHA_DARWIN_ARM64=$(grep "darwin_arm64" "$CHECKSUMS_FILE" | awk '{print $1}' || echo "")
SHA_LINUX_AMD64=$(grep "linux_amd64" "$CHECKSUMS_FILE" | awk '{print $1}' || echo "")
SHA_WINDOWS_AMD64=$(grep "windows_amd64" "$CHECKSUMS_FILE" | awk '{print $1}' || echo "")

echo "Updating marketplace.json for ${VERSION_TAG}"
echo "  darwin_amd64: ${SHA_DARWIN_AMD64:0:16}..."
echo "  darwin_arm64: ${SHA_DARWIN_ARM64:0:16}..."
echo "  linux_amd64:  ${SHA_LINUX_AMD64:0:16}..."
echo "  windows_amd64: ${SHA_WINDOWS_AMD64:0:16}..."

# Rewrite the first plugin entry in marketplace.json with the new version,
# release date, download URLs, and checksums for all four target platforms.
jq --arg v "$VERSION" \
   --arg vt "$VERSION_TAG" \
   --arg d "$RELEASE_DATE" \
   --arg sha_da "$SHA_DARWIN_AMD64" \
   --arg sha_dar "$SHA_DARWIN_ARM64" \
   --arg sha_la "$SHA_LINUX_AMD64" \
   --arg sha_wa "$SHA_WINDOWS_AMD64" \
   '.plugins[0].version = $v |
    .plugins[0].releases.latest = $v |
    .plugins[0].releases.versions[$v] = {
      "releaseDate": $d,
      "downloads": {
        "darwin-amd64": {
          "url": "https://github.com/thebtf/engram-plus/releases/download/\($vt)/engram_\($v)_darwin_amd64.tar.gz",
          "sha256": $sha_da,
          "format": "tar.gz"
        },
        "darwin-arm64": {
          "url": "https://github.com/thebtf/engram-plus/releases/download/\($vt)/engram_\($v)_darwin_arm64.tar.gz",
          "sha256": $sha_dar,
          "format": "tar.gz"
        },
        "linux-amd64": {
          "url": "https://github.com/thebtf/engram-plus/releases/download/\($vt)/engram_\($v)_linux_amd64.tar.gz",
          "sha256": $sha_la,
          "format": "tar.gz"
        },
        "windows-amd64": {
          "url": "https://github.com/thebtf/engram-plus/releases/download/\($vt)/engram_\($v)_windows_amd64.zip",
          "sha256": $sha_wa,
          "format": "zip"
        }
      }
    }' marketplace.json > marketplace.json.tmp && mv marketplace.json.tmp marketplace.json

echo "marketplace.json updated successfully"

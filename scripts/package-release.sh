#!/usr/bin/env bash
# Package cross-compiled binaries from $DIST_DIR into per-platform release
# archives (tar.gz for unix, zip for windows) and produce SHA256SUMS.
#
# Usage: package-release.sh <version> <dist-dir> <platform>...
#   e.g. package-release.sh v0.1.0 bin/dist darwin-arm64 linux-amd64 windows-amd64
set -euo pipefail

VERSION="${1:?usage: $0 <version> <dist-dir> <platform>...}"
DIST_DIR="${2:?usage: $0 <version> <dist-dir> <platform>...}"
shift 2
PLATFORMS=("$@")
if [ ${#PLATFORMS[@]} -eq 0 ]; then
  echo "ERROR: at least one platform required" >&2
  exit 1
fi

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$DIST_DIR"

EXTRAS=()
[ -f "$REPO_ROOT/README.md" ] && EXTRAS+=("$REPO_ROOT/README.md")
[ -f "$REPO_ROOT/LICENSE" ]   && EXTRAS+=("$REPO_ROOT/LICENSE")

for plat in "${PLATFORMS[@]}"; do
  os="${plat%%-*}"
  ext=""
  [ "$os" = "windows" ] && ext=".exe"

  stage="cortex-${VERSION}-${plat}"
  rm -rf "$stage"
  mkdir -p "$stage"

  cp "cortex-${plat}${ext}"     "${stage}/cortex${ext}"
  cp "cortex-mcp-${plat}${ext}" "${stage}/cortex-mcp${ext}"
  [ ${#EXTRAS[@]} -gt 0 ] && cp "${EXTRAS[@]}" "$stage/"

  if [ "$os" = "windows" ]; then
    rm -f "${stage}.zip"
    zip -qr "${stage}.zip" "$stage"
    echo "  ${stage}.zip"
  else
    rm -f "${stage}.tar.gz"
    tar -czf "${stage}.tar.gz" "$stage"
    echo "  ${stage}.tar.gz"
  fi
  rm -rf "$stage"
done

# Checksums — use shasum (Mac/BSD) or sha256sum (Linux), whichever is present.
rm -f SHA256SUMS
if command -v shasum >/dev/null 2>&1; then
  # shellcheck disable=SC2046
  shasum -a 256 $(ls cortex-"${VERSION}"-*.tar.gz cortex-"${VERSION}"-*.zip 2>/dev/null) > SHA256SUMS
else
  # shellcheck disable=SC2046
  sha256sum $(ls cortex-"${VERSION}"-*.tar.gz cortex-"${VERSION}"-*.zip 2>/dev/null) > SHA256SUMS
fi
echo "  SHA256SUMS"

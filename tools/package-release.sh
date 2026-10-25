#!/usr/bin/env bash
# package-release.sh: build the tarballs a GitHub Release ships.
#
#   tools/package-release.sh VERSION OUTDIR [GOOS/GOARCH ...]
#
# VERSION is a tag (v1.2.3, or v1.2.3-rc.1 for a pre-release), or "dev". With
# no targets it builds the four release targets. Each becomes
# OUTDIR/tandem-<goos>-<goarch>.tar.gz. SHA256SUMS is written last, so a
# half-finished run never looks complete. Needs Go; users never run it.
set -euo pipefail

usage() { printf 'usage: %s VERSION OUTDIR [GOOS/GOARCH ...]\n' "$0" >&2; exit 2; }
[ "$#" -ge 2 ] || usage
VERSION=$1; OUT=$2; shift 2
if [ "$VERSION" != dev ] && ! printf '%s\n' "$VERSION" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'; then
  printf 'package-release: %s is not a tag like v1.2.3 or v1.2.3-rc.1\n' "$VERSION" >&2
  exit 2
fi
[ "$#" -gt 0 ] || set -- darwin/arm64 darwin/amd64 linux/amd64 linux/arm64

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
HASH=$("$ROOT/bin/lib/source-hash.sh" "$ROOT")
[ -n "$HASH" ] || { printf 'package-release: the build inputs hashed to nothing\n' >&2; exit 1; }

# macOS tags files with attributes (com.apple.provenance) that bsdtar packs as
# pax headers, and GNU tar on Linux warns about each one while unpacking.
TAR_FLAGS="--no-xattrs"
tar --version 2>/dev/null | grep -q bsdtar && TAR_FLAGS="--no-xattrs --no-mac-metadata"
export COPYFILE_DISABLE=1

mkdir -p "$OUT"
OUT=$(cd "$OUT" && pwd)
STAGE=$(mktemp -d); trap 'rm -rf "$STAGE"' EXIT

for target in "$@"; do
  goos=${target%/*}; goarch=${target#*/}
  dir="$STAGE/$goos-$goarch/tandem"
  mkdir -p "$dir/bin/lib" "$dir/config"
  ( cd "$ROOT" && CGO_ENABLED=0 GOOS=$goos GOARCH=$goarch go build -trimpath \
      -ldflags "-X main.version=$VERSION -X main.sourceHash=$HASH" \
      -o "$dir/tandem" ./cmd/tandem ) \
    || { printf 'package-release: go build failed for %s\n' "$target" >&2; exit 1; }
  # The commands and their helpers. source-hash.sh is left out: it runs Go.
  for f in "$ROOT"/bin/*; do [ -f "$f" ] && cp -p "$f" "$dir/bin/"; done
  for f in "$ROOT"/bin/lib/*; do
    [ "$(basename "$f")" = source-hash.sh ] || cp -p "$f" "$dir/bin/lib/"
  done
  cp "$ROOT/config/config.toml.example" "$dir/config/"
  cp "$ROOT/LICENSE" "$dir/"
  printf '%s\n' "$VERSION" > "$dir/VERSION"
  # shellcheck disable=SC2086
  tar $TAR_FLAGS -C "$STAGE/$goos-$goarch" -czf "$OUT/tandem-$goos-$goarch.tar.gz" tandem
  printf 'package-release: %s/tandem-%s-%s.tar.gz\n' "$OUT" "$goos" "$goarch"
done

( cd "$OUT"
  rm -f SHA256SUMS
  if command -v shasum >/dev/null 2>&1; then shasum -a 256 tandem-*.tar.gz; else sha256sum tandem-*.tar.gz; fi \
    > SHA256SUMS.tmp
  mv SHA256SUMS.tmp SHA256SUMS )
printf 'package-release: %s/SHA256SUMS\n' "$OUT"

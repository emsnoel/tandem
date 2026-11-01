#!/usr/bin/env bash
# tools/package-release.sh builds the tarballs install.sh downloads, so their
# layout, checksums and stamped version are pinned here.
#
# Isolated in one temp dir. Only the host platform and one cross target are
# built.
set -uo pipefail
ROOT="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
PKG="$ROOT/tools/package-release.sh"
PASS=0; FAIL=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }
done_() { printf '\npackage-release: passed %d, failed %d\n' "$PASS" "$FAIL"; [ "$FAIL" -eq 0 ]; exit $?; }

TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT

[ -x "$PKG" ] && ok "tools/package-release.sh exists and is executable" || { nope "tools/package-release.sh is missing or not executable"; done_; }

sha_check() {
  if command -v shasum >/dev/null 2>&1; then shasum -a 256 -c "$@"; else sha256sum -c "$@"; fi
}

HOST="$(go env GOOS)/$(go env GOARCH)"
ASSET="tandem-$(go env GOOS)-$(go env GOARCH).tar.gz"
HASH=$("$ROOT/bin/lib/source-hash.sh" "$ROOT")

OUT="$TMP/out"
LOG=$("$PKG" v9.9.9 "$OUT" "$HOST" 2>&1); RC=$?
[ "$RC" -eq 0 ] && ok "packages the host platform" || { nope "packaging failed (rc $RC): $LOG"; done_; }

# shellcheck disable=SC2012
FILES=$(ls "$OUT" | sort | tr '\n' ' ')
[ "$FILES" = "SHA256SUMS $ASSET " ] && ok "writes one tarball and SHA256SUMS" || nope "OUT holds: $FILES"

( cd "$OUT" && sha_check SHA256SUMS >/dev/null 2>&1 ) && ok "SHA256SUMS verifies the tarball" || nope "SHA256SUMS does not verify"

LIST=$(tar -tzf "$OUT/$ASSET")
for want in tandem/tandem tandem/bin/tandem-pair tandem/bin/lib/reviewer.sh \
            tandem/bin/lib/relay.sh tandem/bin/lib/pair-launch.sh \
            tandem/bin/lib/pair-panes.sh tandem/bin/lib/pair-records.sh \
            tandem/bin/tandem-unpair tandem/bin/lib/unpair-find.sh \
            tandem/bin/lib/unpair-gates.sh tandem/bin/lib/unpair-refusals.sh \
            tandem/bin/lib/unpair-close.sh tandem/bin/tandem-handoff \
            tandem/bin/lib/handoff-gate.sh tandem/bin/lib/handoff-start.sh \
            tandem/bin/lib/handoff-prompt.sh tandem/bin/lib/handoff-prompt.txt \
            tandem/bin/lib/handoff-send.sh \
            tandem/config/config.toml.example tandem/VERSION tandem/LICENSE; do
  printf '%s\n' "$LIST" | grep -qx "$want" && ok "the tarball holds $want" || nope "the tarball is missing $want"
done
printf '%s\n' "$LIST" | grep -q 'source-hash.sh' && nope "the tarball ships bin/lib/source-hash.sh, which needs Go" \
  || ok "the tarball leaves out bin/lib/source-hash.sh"
# macOS tar packs extended attributes that GNU tar warns about. Use a file,
# not a pipe: grep -q would SIGPIPE gzip and pipefail would hide the match.
gzip -dc "$OUT/$ASSET" > "$TMP/raw.tar"
grep -aqE 'SCHILY\.xattr|LIBARCHIVE\.xattr' "$TMP/raw.tar" \
  && nope "the tarball carries macOS file attributes" || ok "the tarball carries no macOS file attributes"

mkdir -p "$TMP/x" && tar -C "$TMP/x" -xzf "$OUT/$ASSET"
GOT=$("$TMP/x/tandem/tandem" version 2>&1)
[ "$GOT" = "tandem v9.9.9 (source ${HASH:0:12})" ] && ok "the binary reports the tag and the source" || nope "tandem version printed '$GOT'"
GOT=$("$TMP/x/tandem/tandem" version --hash 2>&1)
[ "$GOT" = "$HASH" ] && ok "the binary is stamped with the full source hash" || nope "tandem version --hash printed '$GOT'"
[ "$(cat "$TMP/x/tandem/VERSION")" = "v9.9.9" ] && ok "VERSION names the tag" || nope "VERSION reads '$(cat "$TMP/x/tandem/VERSION")'"

# Every module linked into the binary, and Go itself, has its notice shipped.
NOTICES="$TMP/x/tandem/THIRD_PARTY_NOTICES"
if [ -s "$NOTICES" ]; then
  ok "the tarball holds THIRD_PARTY_NOTICES"
  DEPS=$(go version -m "$TMP/x/tandem/tandem" | awk '$1 == "dep" { print $2 "@" $3 }')
  [ -n "$DEPS" ] || nope "the binary lists no dependencies"
  for dep in $DEPS; do
    grep -qxF "== $dep ==" "$NOTICES" && ok "the notices cover $dep" || nope "the notices miss $dep"
  done
  grep -qx '== Go standard library ==' "$NOTICES" && grep -q 'The Go Authors' "$NOTICES" \
    && ok "the notices cover the Go standard library" || nope "the notices miss the Go standard library"
else
  nope "the tarball has no THIRD_PARTY_NOTICES"
fi

for bad in 9.9.9 v9.9; do
  "$PKG" "$bad" "$TMP/bad" "$HOST" >/dev/null 2>&1; RC=$?
  [ "$RC" -eq 2 ] && ok "refuses the malformed tag $bad" || nope "tag $bad exited $RC, want 2"
done

"$PKG" v9.9.9-rc.1 "$TMP/rc" "$HOST" >/dev/null 2>&1 \
  && mkdir -p "$TMP/rcx" && tar -C "$TMP/rcx" -xzf "$TMP/rc/$ASSET" \
  && [ "$(cat "$TMP/rcx/tandem/VERSION")" = "v9.9.9-rc.1" ] \
  && ok "accepts a pre-release tag" || nope "a pre-release tag was not packaged"

# Cross-compiling proves the release needs no C toolchain for a target.
if "$PKG" v9.9.9 "$TMP/cross" linux/amd64 >/dev/null 2>&1; then
  mkdir -p "$TMP/cx" && tar -C "$TMP/cx" -xzf "$TMP/cross/tandem-linux-amd64.tar.gz"
  INFO=$(go version -m "$TMP/cx/tandem/tandem")
  for want in GOOS=linux GOARCH=amd64 CGO_ENABLED=0; do
    printf '%s\n' "$INFO" | grep -q "$want" && ok "the linux/amd64 build has $want" || nope "the linux/amd64 build lacks $want"
  done
else
  nope "the linux/amd64 cross build failed"
fi

done_

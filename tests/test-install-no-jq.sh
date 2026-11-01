#!/usr/bin/env bash
# install.sh --from-source works on a machine with no jq, and so do the
# commands it installs. macOS ships /usr/bin/jq, so the PATH here holds a
# symlink to every command in /bin and /usr/bin except jq, plus go.
#
# Isolated: install.sh writes only to a temp DEST, share dir and Herdr config.
set -uo pipefail
ROOT="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
PASS=0; FAIL=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }

GO=$(command -v go) || { echo "  skip install-no-jq: go is not on PATH"; exit 0; }
TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
NOJQ="$TMP/path"; mkdir -p "$NOJQ"
for d in /bin /usr/bin; do
  for p in "$d"/*; do
    name=${p##*/}
    [ "$name" = jq ] || [ -e "$NOJQ/$name" ] || [ ! -x "$p" ] || ln -s "$p" "$NOJQ/$name"
  done
done
ln -sf "$GO" "$NOJQ/go"
DEST="$TMP/dest"

PATH="$NOJQ" command -v jq >/dev/null 2>&1 && nope "the test PATH still finds jq" || ok "the test PATH has no jq"

OUT=$(PATH="$NOJQ" HERDR_CONFIG_PATH="$TMP/herdr/config.toml" TANDEM_SHARE_DIR="$TMP/share" \
        "$ROOT/install.sh" --from-source "$DEST" 2>&1); RC=$?
[ "$RC" -eq 0 ] && ok "install.sh --from-source succeeds with no jq" || nope "install.sh --from-source with no jq (rc $RC): $OUT"
case "$OUT" in *jq*) nope "install.sh mentions jq: $OUT" ;; *) ok "install.sh does not mention jq" ;; esac
[ -x "$DEST/tandem" ] && ok "it built the binary" || nope "it built no binary"

# The installed commands run with no jq too.
REPO="$TMP/repo"; mkdir -p "$REPO"; git -C "$REPO" init -q
OUT=$(cd "$REPO" && PATH="$DEST:$NOJQ" "$DEST/tandem-init" 2>&1); RC=$?
[ "$RC" -eq 0 ] && [ -f "$REPO/.tandem.json" ] && ok "tandem-init runs with no jq" || nope "tandem-init with no jq (rc $RC): $OUT"
OUT=$(cd "$REPO" && PATH="$DEST:$NOJQ" "$DEST/tandem-relaylog" 2>&1); RC=$?
[ "$RC" -eq 0 ] && ok "tandem-relaylog runs with no jq" || nope "tandem-relaylog with no jq (rc $RC): $OUT"

printf '\ninstall-no-jq: passed %d, failed %d\n' "$PASS" "$FAIL"; [ "$FAIL" -eq 0 ]

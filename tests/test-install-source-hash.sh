#!/usr/bin/env bash
# The source stamp must cover everything the binary is built from, and the
# installer must install commands, not helper directories. One Go helper
# (cmd/tandem-source-hash) computes it for install.sh and test-staleness.sh.
#
# Isolated: install.sh writes only to DEST and HERDR_CONFIG_PATH in a temp dir.
set -uo pipefail
ROOT_DIR=$(cd "$(dirname "$0")/.." && pwd)
HELPER="$ROOT_DIR/bin/lib/source-hash.sh"

PASS=0; FAIL=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }

TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT

[ -x "$HELPER" ] && ok "the shell helper exists and is executable" || { nope "bin/lib/source-hash.sh is missing or not executable"; printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"; exit 1; }

WANT=$("$HELPER") || WANT=""
case "$WANT" in
  [0-9a-f]*) [ "${#WANT}" -eq 64 ] && ok "the helper prints a 64 hex character hash" || nope "the helper printed '$WANT'" ;;
  *)         nope "the helper printed '$WANT'" ;;
esac

# --- installing --------------------------------------------------------------
DEST="$TMP/dest"
OUT=$(HERDR_CONFIG_PATH="$TMP/herdr/config.toml" "$ROOT_DIR/install.sh" --from-source "$DEST" 2>&1); RC=$?
[ "$RC" -eq 0 ] && ok "install.sh succeeds into an isolated directory" || nope "install.sh failed (rc $RC): $OUT"

# The installer must not install bin/lib.
[ -d "$ROOT_DIR/bin/lib" ] || nope "bin/lib is expected to exist for this check"
if [ -e "$DEST/lib" ] || [ -L "$DEST/lib" ]; then
  nope "install.sh created a 'lib' entry in the install directory"
else
  ok "install.sh does not install bin/lib as a command"
fi
[ -L "$DEST/tandem-ask" ] && ok "real commands are still symlinked" || nope "tandem-ask was not installed"

GOT=$("$DEST/tandem" version --hash 2>/dev/null)
[ "$GOT" = "$WANT" ] && ok "the installed binary is stamped with the helper's hash" || nope "stamp '$GOT' differs from helper '$WANT'"

# --- what the stamp covers ---------------------------------------------------
# A copy of the inputs only; the real tree is never modified.
COPY="$TMP/copy"
mkdir -p "$COPY"
cp -R "$ROOT_DIR/cmd" "$ROOT_DIR/internal" "$ROOT_DIR/go.mod" "$COPY/"
[ -f "$ROOT_DIR/go.sum" ] && cp "$ROOT_DIR/go.sum" "$COPY/"
[ "$("$HELPER" "$COPY")" = "$WANT" ] && ok "a copy at another path hashes the same" || nope "a copy at another path hashed differently"

SQL=$(find "$COPY/internal" -name '*.sql' | head -1)
if [ -n "$SQL" ]; then
  printf -- '-- touched\n' >> "$SQL"
  CH=$("$HELPER" "$COPY")
  [ "$CH" != "$WANT" ] && ok "changing only a SQL migration changes the hash" || nope "a SQL change was not noticed"
  [ "$GOT" != "$CH" ] && ok "so an install built before the change reads as stale" || nope "a stale binary would pass"
  cp "$ROOT_DIR/${SQL#$COPY/}" "$SQL"
fi
if [ -f "$COPY/go.sum" ]; then
  printf 'example.com/x v0.0.0 h1:x\n' >> "$COPY/go.sum"
  [ "$("$HELPER" "$COPY")" != "$WANT" ] && ok "changing only go.sum changes the hash" || nope "a go.sum change was not noticed"
fi

printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

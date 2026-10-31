#!/usr/bin/env bash
# The relay mailbox must never reach version control.
#
# tandem-pair creates the mailbox, so it ignores it. The test runs the real
# ensure_relay_ignored() lifted from bin/lib/relay.sh.
set -uo pipefail
SRC="${1:-$(cd "$(dirname "$0")/.." && pwd)/bin/lib/relay.sh}"
[ -r "$SRC" ] || { echo "not readable: $SRC" >&2; exit 2; }

PASS=0; FAIL=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }

info() { :; }   # stub the script's logger
eval "$(sed -n '/^ensure_relay_ignored()/,/^}/p' "$SRC")"
type ensure_relay_ignored >/dev/null 2>&1 \
  || { echo "FATAL: ensure_relay_ignored() not found in $SRC" >&2; exit 2; }

command -v tandem >/dev/null 2>&1 \
  || { echo "  FAIL tandem not on PATH; ensure_relay_ignored asks it for the mailbox name" >&2; exit 1; }

TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
newrepo() { local d="$TMP/$1"; mkdir -p "$d"; git -C "$d" init -q; printf '%s' "$d"; }
# Ask tandem for the name, so the test follows a rename.
NAME=$(basename "$(tandem paths relay --root "$TMP")")
LEGACY=".herdr-relay"
# `grep -c` prints 0 and exits 1 on no match, so `|| printf 0` would give "00".
matches() { # matches <repo> <name>
  [ -f "$1/.gitignore" ] || { printf 0; return; }
  grep -cE "^/?${2//./\\.}/?\$" "$1/.gitignore" 2>/dev/null || true
}
count()  { matches "$1" "$NAME"; }
countl() { matches "$1" "$LEGACY"; }

r=$(newrepo fresh); ensure_relay_ignored "$r"
[ "$(count "$r")" = 1 ] && ok "creates .gitignore with the entry" || nope "creates .gitignore with the entry"

r=$(newrepo existing); printf 'node_modules/\n*.log\n' > "$r/.gitignore"; ensure_relay_ignored "$r"
[ "$(count "$r")" = 1 ] && ok "appends to an existing .gitignore" || nope "appends to an existing .gitignore"
grep -q '^node_modules/$' "$r/.gitignore" && ok "preserves existing patterns" || nope "preserves existing patterns"

r=$(newrepo idempotent); ensure_relay_ignored "$r"; ensure_relay_ignored "$r"; ensure_relay_ignored "$r"
[ "$(count "$r")" = 1 ] && ok "idempotent across repeated pairings" || nope "idempotent (got $(count "$r"))"

r=$(newrepo slashform); printf '/%s/\n' "$NAME" > "$r/.gitignore"; ensure_relay_ignored "$r"
[ "$(count "$r")" = 1 ] && ok "recognises the /-prefixed form" || nope "recognises the /-prefixed form"

# A .gitignore without a trailing newline must not fuse the comment onto its
# last pattern.
r=$(newrepo nonewline); printf 'dist/' > "$r/.gitignore"; ensure_relay_ignored "$r"
grep -q '^dist/$' "$r/.gitignore" && ok "does not corrupt a file lacking a trailing newline" \
  || nope "does not corrupt a file lacking a trailing newline"
[ "$(count "$r")" = 1 ] && ok "still adds the entry in that case" || nope "still adds the entry in that case"

# What matters is that git honours it.
r=$(newrepo effective); ensure_relay_ignored "$r"; mkdir -p "$r/$NAME/q"; : > "$r/$NAME/q/x.json"
git -C "$r" status --porcelain | grep -q "$NAME" \
  && nope "git genuinely ignores the mailbox" || ok "git genuinely ignores the mailbox"

# --- a repo that paired before the rename ---
# Its live mailbox is .herdr-relay, so that is the name to ignore.
r=$(newrepo legacy); mkdir -p "$r/$LEGACY/q"; ensure_relay_ignored "$r"
[ "$(countl "$r")" = 1 ] && ok "ignores the pre-rename mailbox a repo already has" \
  || nope "ignores the pre-rename mailbox a repo already has"
[ "$(count "$r")" = 0 ] && ok "does not add the new name alongside it" \
  || nope "does not add the new name alongside it"
mkdir -p "$r/$LEGACY/q"; : > "$r/$LEGACY/q/x.json"
git -C "$r" status --porcelain | grep -q "$LEGACY" \
  && nope "git ignores the pre-rename mailbox too" || ok "git ignores the pre-rename mailbox too"

# With both present the new name wins, and that is what gets ignored.
r=$(newrepo both); mkdir -p "$r/$LEGACY" "$r/$NAME"; ensure_relay_ignored "$r"
[ "$(count "$r")" = 1 ] && ok "prefers the new name when a repo has both" \
  || nope "prefers the new name when a repo has both"

d="$TMP/plain"; mkdir -p "$d"; ensure_relay_ignored "$d"
[ -f "$d/.gitignore" ] && nope "leaves non-git dirs alone" || ok "leaves non-git dirs alone"

echo; printf 'relay-ignore: passed %d, failed %d\n' "$PASS" "$FAIL"; [ "$FAIL" -eq 0 ]

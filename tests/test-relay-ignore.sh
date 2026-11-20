#!/usr/bin/env bash
# The relay mailbox must never reach version control, and ignoring it must never
# edit a tracked file.
#
# tandem-pair creates the mailbox, so it ignores it. The entry goes to git's
# exclude file: a run stops when a tracked file such as .gitignore changes
# after tandem measured the tree (#91). The test runs the real git_exclude()
# and ensure_relay_ignored() from bin/lib/git-exclude.sh.
set -uo pipefail
SRC="${1:-$(cd "$(dirname "$0")/.." && pwd)/bin/lib/git-exclude.sh}"
[ -r "$SRC" ] || { echo "not readable: $SRC" >&2; exit 2; }

PASS=0; FAIL=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }

INFO=""
info() { INFO="$INFO$*"$'\n'; }   # record the script's logger
die()  { echo "die: $*" >&2; exit 1; }
# shellcheck source=../bin/lib/git-exclude.sh
. "$SRC"
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
matches() { # matches <file> <name>
  [ -f "$1" ] || { printf 0; return; }
  grep -cE "^/?${2//./\\.}/?\$" "$1" 2>/dev/null || true
}
count()  { matches "$1/.git/info/exclude" "$NAME"; }
countl() { matches "$1/.git/info/exclude" "$LEGACY"; }
ignored() { [ -z "$(git -C "$1" status --porcelain)" ]; }

r=$(newrepo fresh); ensure_relay_ignored "$r"
[ "$(count "$r")" = 1 ] && ok "adds the entry to the exclude file" || nope "adds the entry to the exclude file"
[ ! -e "$r/.gitignore" ] && ok "creates no .gitignore" || nope "created a .gitignore"
case "$INFO" in *"added $NAME/ to "*"/info/exclude"*) ok "says where it added the entry" ;; *) nope "says where it added the entry: $INFO" ;; esac

r=$(newrepo tracked); printf 'node_modules/\n' > "$r/.gitignore"
git -C "$r" add .gitignore && git -C "$r" -c user.name=t -c user.email=t@t commit -qm init
ensure_relay_ignored "$r"
mkdir -p "$r/$NAME/q"; : > "$r/$NAME/q/x.json"
ignored "$r" && ok "leaves a tracked .gitignore unchanged and git ignores the mailbox" \
  || nope "the checkout changed: $(git -C "$r" status --porcelain)"

r=$(newrepo idempotent); ensure_relay_ignored "$r"; ensure_relay_ignored "$r"; ensure_relay_ignored "$r"
[ "$(count "$r")" = 1 ] && ok "idempotent across repeated pairings" || nope "idempotent (got $(count "$r"))"

r=$(newrepo gitignored); printf '/%s/\n' "$NAME" > "$r/.gitignore"; ensure_relay_ignored "$r"
[ "$(count "$r")" = 0 ] && ok "adds nothing when .gitignore already ignores it" || nope "added an entry beside .gitignore's"

# An exclude file without a trailing newline must not fuse the comment onto its
# last pattern.
r=$(newrepo nonewline); printf 'dist/' > "$r/.git/info/exclude"; ensure_relay_ignored "$r"
grep -q '^dist/$' "$r/.git/info/exclude" && ok "does not corrupt a file lacking a trailing newline" \
  || nope "does not corrupt a file lacking a trailing newline"
[ "$(count "$r")" = 1 ] && ok "still adds the entry in that case" || nope "still adds the entry in that case"

# A linked worktree reads the exclude file of the common git dir.
r=$(newrepo main); git -C "$r" -c user.name=t -c user.email=t@t commit -q --allow-empty -m init
w="$TMP/linked"; git -C "$r" worktree add -q "$w" 2>/dev/null
ensure_relay_ignored "$w"
[ "$(count "$r")" = 1 ] && ok "a worktree writes the common exclude file" || nope "a worktree writes the common exclude file"
mkdir -p "$w/$NAME/q"; : > "$w/$NAME/q/x.json"
ignored "$w" && ok "git ignores the worktree's mailbox" || nope "the worktree shows: $(git -C "$w" status --porcelain)"
[ ! -e "$w/.gitignore" ] && ok "creates no .gitignore in the worktree" || nope "created a .gitignore in the worktree"

INFO=""; r=$(newrepo unwritable); rm -rf "$r/.git/info/exclude"; mkdir -p "$r/.git/info/exclude"
ensure_relay_ignored "$r"
case "$INFO" in *"WARNING could not write git's exclude file"*) ok "warns when it cannot write" ;; *) nope "warns when it cannot write: $INFO" ;; esac
[ ! -e "$r/.gitignore" ] && ok "and does not fall back to .gitignore" || nope "fell back to .gitignore"

# --- a repo that paired before the rename ---
# Its live mailbox is .herdr-relay, so that is the name to ignore.
r=$(newrepo legacy); mkdir -p "$r/$LEGACY/q"; ensure_relay_ignored "$r"
[ "$(countl "$r")" = 1 ] && ok "ignores the pre-rename mailbox a repo already has" \
  || nope "ignores the pre-rename mailbox a repo already has"
[ "$(count "$r")" = 0 ] && ok "does not add the new name alongside it" \
  || nope "does not add the new name alongside it"
: > "$r/$LEGACY/q/x.json"
ignored "$r" && ok "git ignores the pre-rename mailbox too" || nope "git ignores the pre-rename mailbox too"

# With both present the new name wins, and that is what gets ignored.
r=$(newrepo both); mkdir -p "$r/$LEGACY" "$r/$NAME"; ensure_relay_ignored "$r"
[ "$(count "$r")" = 1 ] && ok "prefers the new name when a repo has both" \
  || nope "prefers the new name when a repo has both"

d="$TMP/plain"; mkdir -p "$d"; ensure_relay_ignored "$d"
[ -z "$(ls -A "$d")" ] && ok "leaves non-git dirs alone" || nope "leaves non-git dirs alone"

echo; printf 'relay-ignore: passed %d, failed %d\n' "$PASS" "$FAIL"; [ "$FAIL" -eq 0 ]

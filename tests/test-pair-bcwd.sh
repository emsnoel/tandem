#!/usr/bin/env bash
# resolve_bcwd (bin/lib/pair-records.sh) reads the builder pane's cwd from
# `tandem agent snapshot`, which prints pane, cwd and status separated by tabs.
# Herdr can report no cwd for a pane, and then the status must not be taken for
# the cwd. The answers of tandem are stubbed.
set -uo pipefail
ROOT_DIR=$(cd "$(dirname "$0")/.." && pwd)
PASS=0; FAIL=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }

TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
mkdir -p "$TMP/bin" "$TMP/repo/sub"; git -C "$TMP/repo" init -q
REPO=$(cd "$TMP/repo" && pwd -P)
# shellcheck disable=SC2016 # the stub expands its own variable
printf '#!/usr/bin/env bash\n[ "$1 $2" = "agent snapshot" ] || exit 2\nprintf "w1:p2\\t%%s\\tidle\\n" "$SNAP_CWD"\n' > "$TMP/bin/tandem"
chmod +x "$TMP/bin/tandem"

# bcwd SNAP_CWD: run resolve_bcwd in REPO/sub with the builder at SNAP_CWD, and
# print BCWD.
bcwd() {
  (cd "$REPO/sub" && env PATH="$TMP/bin:/usr/bin:/bin" SNAP_CWD="$1" bash -c '
    info() { printf "tandem-pair: %s\n" "$*"; }; die() { printf "tandem-pair: %s\n" "$*" >&2; exit 1; }
    . "$1/bin/lib/pair-identity.sh"; . "$1/bin/lib/pair-records.sh"
    DRY_RUN=0 CWD_OVERRIDE="" BUILDER_NAME=builder ROOT="$2"; resolve_bcwd; printf "%s\n" "$BCWD"' _ "$ROOT_DIR" "$REPO" 2>&1)
}

printf '\n== the builder pane cwd ==\n'
OUT=$(bcwd "$REPO"); [ "$OUT" = "$REPO" ] && ok "a reported cwd is used" || nope "reported cwd: $OUT"
OUT=$(bcwd ""); [ "$OUT" = "$REPO/sub" ] && ok "no reported cwd falls back to the planner's folder, not the status" \
  || nope "empty cwd: $OUT"

printf '\ntests/test-pair-bcwd: passed %d, failed %d\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

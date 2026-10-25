#!/usr/bin/env bash
# tandem agent watch through the shipped binary, the fake CLI and the fake
# event socket: a status change wakes it, a gone agent exits 27, and nothing
# happening is a quiet timeout.
set -uo pipefail
ROOT_DIR=$(cd "$(dirname "$0")/.." && pwd)
PASS=0; FAIL=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }
TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
(cd "$ROOT_DIR" && go build -o "$TMP/bin/tandem" ./cmd/tandem) || { echo "could not build tandem"; exit 1; }
ln -s "$(command -v jq)" "$TMP/bin/jq"
. "$ROOT_DIR/tests/lib/fake-herdr.sh"
. "$ROOT_DIR/tests/lib/fake-socket.sh"
export FAKE="$TMP/fake" REPO="$TMP"
fake_herdr_install "$TMP/bin" "$FAKE"
export PATH="$TMP/bin:/usr/bin:/bin"
fake_socket_start "$TMP" || exit 1

# Drive the status until the watch returns, so the change cannot land before
# the watch has subscribed and snapshotted.
( while :; do fake_set_state working; sleep 0.1; fake_set_state idle; sleep 0.1; done ) &
DRIVER=$!
out=$(tandem agent watch builder --timeout 5000); rc=$?
kill "$DRIVER" 2>/dev/null; wait "$DRIVER" 2>/dev/null
fake_set_state idle
case "$out" in "$(printf 'changed\tbuilder\t')"*) hit=1 ;; *) hit=0 ;; esac
[ "$rc" = 0 ] && [ "$hit" = 1 ] && ok "a status change wakes it" || nope "status change: rc=$rc out=$out"
sleep 0.3

out=$(tandem agent watch builder --timeout 300); rc=$?
[ "$rc" = 0 ] && [ "$out" = timeout ] && ok "nothing happening is a timeout" || nope "timeout: rc=$rc out=$out"

( sleep 0.3; printf 'builder\n' > "$FAKE/absent" ) &
out=$(tandem agent watch builder --timeout 3000); rc=$?
[ "$rc" = 27 ] && [ "$out" = "$(printf 'agent_exited\tbuilder')" ] && ok "a gone agent exits 27" || nope "gone: rc=$rc out=$out"

printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" = 0 ]

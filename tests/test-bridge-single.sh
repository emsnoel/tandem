#!/usr/bin/env bash
# One tandem-bridge per mailbox. Two bridges on one relay would each tell the
# planner about the same pause.
#
#   - a second start on the same mailbox refuses, and the first keeps running;
#   - a bridge waiting for events shows as one process, not two;
#   - a pid file left by a bridge that is gone is taken over;
#   - tandem-pair's start_bridge reuses a running bridge and records a new one.
#
# A fake Herdr and its event socket stand in for Herdr. Every bridge started
# here is stopped by its pid, with its children, when the test ends.
set -uo pipefail
ROOT_DIR=$(cd "$(dirname "$0")/.." && pwd)

PASS=0; FAIL=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }
has()  { case "$2" in *"$3"*) ok "$1" ;; *) nope "$1 (missing: $3): $2" ;; esac; }

TMP=$(mktemp -d)
STARTED=""
stop_all() {
  local p
  for p in $STARTED $(cat "$MB/bridge.pid" 2>/dev/null); do
    pkill -P "$p" 2>/dev/null; kill "$p" 2>/dev/null
  done
  for p in $STARTED; do wait "$p" 2>/dev/null; done
}
trap 'stop_all; rm -rf "$TMP"' EXIT
(cd "$ROOT_DIR" && go build -o "$TMP/bin/tandem" ./cmd/tandem) || { echo "could not build tandem"; exit 1; }
. "$ROOT_DIR/tests/lib/fake-herdr.sh"
. "$ROOT_DIR/tests/lib/fake-socket.sh"
FAKE="$TMP/fake"
fake_herdr_install "$TMP/bin" "$FAKE"
FAKE="$FAKE" PATH="$TMP/bin:$PATH" fake_socket_start "$TMP" || exit 1

R="$TMP/repo"; MB="$R/.tandem-relay"
mkdir -p "$MB"; git -C "$R" init -q
BENV=(env PATH="$TMP/bin:$ROOT_DIR/bin:/usr/bin:/bin" FAKE="$FAKE" REPO="$R" HERDR_ENV=1 HERDR_BRIDGE_BACKSTOP_SECONDS=30)

# bridge: start a looping bridge on the mailbox in the background; BPID is its pid.
bridge() {
  "${BENV[@]}" "$ROOT_DIR/bin/tandem-bridge" --mailbox "$MB" --planner planner >>"$TMP/bridge.$1.log" 2>&1 &
  BPID=$!; STARTED="$STARTED $BPID"
}
# ready: wait up to 5 s for the bridge's ready file.
ready() { local i; for i in $(seq 1 50); do [ -f "$MB/bridge.ready" ] && return 0; sleep 0.1; done; return 1; }
# exits_within PID: the process ends within 5 s.
exits_within() { local i; for i in $(seq 1 50); do kill -0 "$1" 2>/dev/null || return 0; sleep 0.1; done; return 1; }
# bridges: processes whose command line is a bridge on this mailbox.
bridges() { pgrep -f "tandem-bridge --mailbox $MB" | wc -l | tr -d ' '; }

printf '== a second start on the same mailbox ==\n'
bridge first; FIRST=$BPID
ready && ok "the first bridge starts" || nope "the first bridge is not ready: $(cat "$TMP/bridge.first.log")"
[ "$(cat "$MB/bridge.pid" 2>/dev/null)" = "$FIRST" ] && ok "it records its own pid" || nope "bridge.pid: $(cat "$MB/bridge.pid" 2>/dev/null), want $FIRST"
bridge second; SECOND=$BPID
if exits_within "$SECOND"; then
  wait "$SECOND"; rc=$?
  [ "$rc" -ne 0 ] && ok "the second refuses with a non-zero exit" || nope "the second exited 0"
else
  nope "the second bridge kept running"
  pkill -P "$SECOND" 2>/dev/null; kill "$SECOND" 2>/dev/null
fi
has "and says which bridge already watches the mailbox" "$(cat "$TMP/bridge.second.log")" "another tandem-bridge (pid $FIRST) already watches"
kill -0 "$FIRST" 2>/dev/null && ok "the first keeps running" || nope "the first bridge stopped"
[ "$(cat "$MB/bridge.pid" 2>/dev/null)" = "$FIRST" ] && ok "and still owns the pid file" || nope "bridge.pid: $(cat "$MB/bridge.pid" 2>/dev/null)"

printf '\n== a waiting bridge is one process ==\n'
# The wait runs `tandem agent watch` in a command substitution. Without exec,
# bash 3.2 forks a copy of the bridge to run it, which ps shows as a second
# bridge on the same mailbox.
sleep 1
n=0; for _ in 1 2 3 4 5; do [ "$(bridges)" = 1 ] && n=$((n+1)); sleep 0.3; done
[ "$n" = 5 ] && ok "ps shows one bridge on the mailbox" || nope "ps showed more than one bridge in $((5-n)) of 5 looks: $(pgrep -fl "tandem-bridge --mailbox $MB")"

printf '\n== a pid file left by a bridge that is gone ==\n'
pkill -P "$FIRST" 2>/dev/null; kill "$FIRST" 2>/dev/null; wait "$FIRST" 2>/dev/null
[ ! -e "$MB/bridge.pid" ] && ok "a stopped bridge removes its pid file" || nope "bridge.pid: $(cat "$MB/bridge.pid" 2>/dev/null)"
# A bridge killed with no chance to clean up, as by SIGKILL, leaves its pid.
printf '%s\n' "$FIRST" > "$MB/bridge.pid"
rm -f "$MB/bridge.ready"
bridge third; THIRD=$BPID
ready && kill -0 "$THIRD" 2>/dev/null && ok "a new bridge starts over a dead pid" || nope "the new bridge did not start: $(cat "$TMP/bridge.third.log")"
[ "$(cat "$MB/bridge.pid" 2>/dev/null)" = "$THIRD" ] && ok "and takes the pid file" || nope "bridge.pid: $(cat "$MB/bridge.pid" 2>/dev/null), want $THIRD"
pkill -P "$THIRD" 2>/dev/null; kill "$THIRD" 2>/dev/null; wait "$THIRD" 2>/dev/null

printf '\n== a bridge that ends by itself gives the mailbox back ==\n'
"${BENV[@]}" "$ROOT_DIR/bin/tandem-bridge" --mailbox "$MB" --planner planner --once >>"$TMP/bridge.once.log" 2>&1
[ ! -e "$MB/bridge.pid" ] && ok "--once removes its pid file" || nope "bridge.pid left: $(cat "$MB/bridge.pid")"

printf "\n== tandem-pair's start_bridge ==\n"
# start_bridge with the globals tandem-pair sets.
pair_start() {
  "${BENV[@]}" bash -c '
    info() { printf "tandem-pair: %s\n" "$*"; }; die() { info "$*"; exit 1; }
    . "$1/bin/lib/relay.sh"; . "$1/bin/lib/pair-records.sh"; . "$1/bin/lib/bridge-lock.sh"
    DRY_RUN=0 MB_ROOT="$2" BCWD="$2" PLANNER_NAME=planner; start_bridge' _ "$ROOT_DIR" "$R" 2>&1
}
OUT=$(pair_start)
PAIRED=$(cat "$MB/bridge.pid" 2>/dev/null)
has "it starts a bridge when none runs" "$OUT" "relay bridge started (pid $PAIRED)"
kill -0 "$PAIRED" 2>/dev/null && ok "and that bridge runs" || nope "no bridge at pid $PAIRED: $OUT"
OUT=$(pair_start)
has "a second pair reuses it" "$OUT" "relay bridge already running (pid $PAIRED)"
[ "$(bridges)" = 1 ] && ok "and starts no second bridge" || nope "bridges: $(pgrep -fl "tandem-bridge --mailbox $MB")"

printf '\ntests/test-bridge-single: passed %d, failed %d\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

#!/usr/bin/env bash
# tandem-ask's mailbox wait must tell a dead tandem-bridge from a slow peer.
#
# Hermetic: tandem, herdr and ps are stubbed; jq is real. ps is stubbed because
# a sandbox can deny the process table while allowing kill -0. PS_MATCH_PID and
# PS_MATCH_NAME choose what the stub reports; PS_DENY makes it fail.
set -uo pipefail
ASK="${1:-$(cd "$(dirname "$0")/.." && pwd)/bin/tandem-ask}"
[ -x "$ASK" ] || { echo "not executable: $ASK" >&2; exit 2; }

PASS=0; FAIL=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }
chk()  { case "$OUT" in *"$2"*) ok "$1" ;; *) nope "$1 (missing: $2)" ;; esac; }
nchk() { case "$OUT" in *"$2"*) nope "$1 (unexpected: $2)" ;; *) ok "$1" ;; esac; }

TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
mkdir -p "$TMP/bin" "$TMP/repo/.tandem-relay"
REPO="$TMP/repo"
git -C "$REPO" init -q

# Forces mailbox mode: agent snapshot exits 3, and paths relay echoes back its
# --root.
cat > "$TMP/bin/tandem" <<'STUB'
#!/usr/bin/env bash
case "$1 $2" in
  "agent snapshot") exit 3 ;;
esac
if [ "$1" = "paths" ] && [ "$2" = "relay" ]; then
  shift 2
  root=""
  while [ $# -gt 0 ]; do
    case "$1" in --root) root="$2"; shift 2 ;; *) shift ;; esac
  done
  printf '%s/.tandem-relay\n' "$root"
  exit 0
fi
exit 0
STUB
chmod +x "$TMP/bin/tandem"
printf '#!/usr/bin/env bash\nprintf "{}"\n' > "$TMP/bin/herdr"
chmod +x "$TMP/bin/herdr"

# A fake ps: PS_MATCH_PID/PS_MATCH_NAME report a chosen name for a chosen pid,
# anything else is "unrelated-process". PS_DENY fails with empty output, which
# tandem-ask must read as "cannot confirm", not "not a bridge".
cat > "$TMP/bin/ps" <<'STUB'
#!/usr/bin/env bash
[ "${1:-}" = -p ] && [ "${3:-}" = -o ] && [ "${4:-}" = command= ] || exit 2
if [ -n "${PS_DENY:-}" ]; then
  echo "ps: operation not permitted" >&2
  exit 1
fi
if [ "${2:-}" = "${PS_MATCH_PID:-}" ]; then
  printf '%s\n' "${PS_MATCH_NAME:-tandem-bridge}"
else
  printf 'unrelated-process\n'
fi
STUB
chmod +x "$TMP/bin/ps"

# --timeout 3 with a 1-second heartbeat. The loop counts whole seconds, so a
# 2-second timeout under load can skip the beat entirely.
run_ask() {
  OUT=$(cd "$REPO" && PATH="$TMP/bin:$PATH" HERDR_ENV=1 HERDR_PANE_ID=pX HERDR_ROLE=builder \
    HERDR_ASK_HEARTBEAT_SECONDS=1 PS_DENY="${PS_DENY:-}" \
    PS_MATCH_PID="${PS_MATCH_PID:-}" PS_MATCH_NAME="${PS_MATCH_NAME:-}" \
    "$ASK" --timeout 3 planner "q" 2>&1); RC=$?
}

echo "--- bridge never started (no bridge.pid at all) ---"
run_ask
[ "$RC" -eq 5 ] && ok "exits 5 on timeout" || nope "exits 5 on timeout (got $RC)"
chk "heartbeat calls out the dead bridge, not a generic wait" "tandem-bridge is not running"
chk "final message names the dead bridge as the likely cause" "and tandem-bridge is not running"
chk "final message tells the caller how to recover" "re-running tandem-pair"
nchk "heartbeat does not print the generic still-waiting line" "still waiting for planner —"
rm -f "$REPO/.tandem-relay"/q/*.json 2>/dev/null

echo "--- bridge pid file present but stale (process does not exist) ---"
echo 99999999 > "$REPO/.tandem-relay/bridge.pid"
run_ask
chk "a stale pid is treated the same as no bridge at all" "tandem-bridge is not running"
rm -f "$REPO/.tandem-relay/bridge.pid" "$REPO/.tandem-relay"/q/*.json 2>/dev/null

echo "--- bridge pid file present but belongs to an unrelated process ---"
sleep 5 & OTHERPID=$!
echo "$OTHERPID" > "$REPO/.tandem-relay/bridge.pid"
PS_MATCH_PID="" run_ask
chk "a pid that is alive but not a tandem-bridge process is not trusted" "tandem-bridge is not running"
kill "$OTHERPID" 2>/dev/null; wait "$OTHERPID" 2>/dev/null
rm -f "$REPO/.tandem-relay/bridge.pid" "$REPO/.tandem-relay"/q/*.json 2>/dev/null

echo "--- a real running tandem-bridge process: peer is just slow ---"
sleep 30 & BRIDGE_PID=$!
echo "$BRIDGE_PID" > "$REPO/.tandem-relay/bridge.pid"
PS_MATCH_PID="$BRIDGE_PID" PS_MATCH_NAME="tandem-bridge" run_ask
[ "$RC" -eq 5 ] && ok "still exits 5 on timeout" || nope "exits 5 on timeout (got $RC)"
chk "heartbeat uses the generic still-waiting message when the bridge is alive" "still waiting for planner —"
chk "final message blames a slow peer, not the bridge" "peer itself is likely just busy or slow"
nchk "final message does not falsely blame the bridge" "and tandem-bridge is not running"
kill "$BRIDGE_PID" 2>/dev/null; wait "$BRIDGE_PID" 2>/dev/null
rm -f "$REPO/.tandem-relay/bridge.pid" "$REPO/.tandem-relay"/q/*.json 2>/dev/null

echo "--- a sandbox that denies the process table entirely (Codex seatbelt) ---"
sleep 30 & BRIDGE_PID=$!
echo "$BRIDGE_PID" > "$REPO/.tandem-relay/bridge.pid"
PS_DENY=1 run_ask
chk "a live pid is still trusted when ps itself is denied" "still waiting for planner —"
nchk "ps being denied must not be reported as a dead bridge" "tandem-bridge is not running"
kill "$BRIDGE_PID" 2>/dev/null; wait "$BRIDGE_PID" 2>/dev/null
rm -f "$REPO/.tandem-relay/bridge.pid" "$REPO/.tandem-relay"/q/*.json 2>/dev/null

printf '\nask-bridge-liveness: passed %d, failed %d\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

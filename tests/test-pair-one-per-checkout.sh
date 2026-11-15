#!/usr/bin/env bash
# One pair per checkout. The relay holds one builder.json, one reviewer.json, one
# pair.json and one bridge, so a second planner in the same checkout would
# take over the first pair's verdicts and handbacks. tandem-pair refuses that
# second planner before it changes anything (check_relay_owner).
#
# The Herdr answers are stubbed: `tandem agent snapshot NAME` prints the pane
# holding NAME, or exits 10 when no agent has it. `tandem paths names` is the
# branch's own binary.
set -uo pipefail
ROOT_DIR=$(cd "$(dirname "$0")/.." && pwd)
PASS=0; FAIL=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }

TMP=$(mktemp -d); trap 'pkill -P "$BRIDGE" 2>/dev/null; kill "$BRIDGE" 2>/dev/null; rm -rf "$TMP"' EXIT
MB="$TMP/.tandem-relay"; mkdir -p "$MB" "$TMP/bin"
# A process whose command line names tandem-bridge stands in for a live bridge.
# shellcheck disable=SC2016 # the stub expands its own argument
printf '#!/usr/bin/env bash\nsleep "$1"\n' > "$TMP/bin/tandem-bridge"; chmod +x "$TMP/bin/tandem-bridge"
"$TMP/bin/tandem-bridge" 300 >/dev/null 2>&1 & BRIDGE=$!
(cd "$ROOT_DIR" && go build -o "$TMP/tandem-real" ./cmd/tandem) || { echo "could not build tandem"; exit 1; }
cat > "$TMP/bin/tandem" <<STUB
#!/usr/bin/env bash
[ "\$1 \$2" = "paths names" ] && exec "$TMP/tandem-real" "\$@"
[ "\$1 \$2" = "agent snapshot" ] || exit 2
case "\$3" in \${LIVE:-none}) printf "w1:p7\\t/repo\\tidle\\n" ;; *) exit 10 ;; esac
STUB
chmod +x "$TMP/bin/tandem"

# check NAME PANE [LIVE]: run check_relay_owner as planner NAME in PANE, with
# the agent LIVE holding pane w1:p7.
check() {
  env PATH="$TMP/bin:/usr/bin:/bin" LIVE="${3:-none}" bash -c '
    info() { printf "tandem-pair: %s\n" "$*"; }; die() { printf "tandem-pair: %s\n" "$*" >&2; exit 1; }
    . "$1/bin/lib/bridge-lock.sh"; . "$1/bin/lib/pair-identity.sh"
    PLANNER_NAME="$3" PLANNER_PANE="$4"; check_relay_owner "$2"' _ "$ROOT_DIR" "$MB" "$1" "$2" 2>&1
}

printf '\n== no live pair ==\n'
OUT=$(check planner-2 w1:p8 planner); rc=$?
[ "$rc" -eq 0 ] && ok "a relay with no bridge pid is free" || nope "rc $rc: $OUT"
printf '999999\n' > "$MB/bridge.pid"
OUT=$(check planner-2 w1:p8 planner); rc=$?
[ "$rc" -eq 0 ] && ok "a dead bridge pid is free" || nope "rc $rc: $OUT"

printf '\n== a live pair ==\n'
printf '%s\n' "$BRIDGE" > "$MB/bridge.pid"; printf '{"planner":"planner-1","builder":"builder","reviewer":"reviewer"}\n' > "$MB/pair.json"
OUT=$(check planner-1 w1:p7 planner-1); rc=$?
[ "$rc" -eq 0 ] && ok "its own planner pairs again" || nope "rc $rc: $OUT"
OUT=$(check planner-2 w1:p8 planner-1); rc=$?
[ "$rc" -ne 0 ] && ok "another planner is refused" || nope "rc $rc: $OUT"
case "$OUT" in *"planner-1"*"w1:p7"*) ok "and the refusal names the owner and its pane" ;; *) nope "owner not named: $OUT" ;; esac
case "$OUT" in *"git worktree"*) ok "and points at a separate worktree" ;; *) nope "no worktree remedy: $OUT" ;; esac
OUT=$(check planner-2 w1:p7 planner-1); rc=$?
[ "$rc" -eq 0 ] && ok "the owner's own pane under a new name pairs again" || nope "rc $rc: $OUT"
# The second planner's own HERDR_PLANNER_NAME says nothing about who owns the relay.
OUT=$(HERDR_PLANNER_NAME=planner-2 check planner-2 w1:p8 planner-1); rc=$?
[ "$rc" -ne 0 ] && case "$OUT" in *"'planner-1' in pane w1:p7"*) true ;; *) false ;; esac \
  && ok "the owner comes from pair.json, not this pane's HERDR_PLANNER_NAME" || nope "rc $rc: $OUT"

printf '\n== a live bridge whose planner is gone ==\n'
OUT=$(check planner-2 w1:p8 none); rc=$?
[ "$rc" -ne 0 ] && ok "it is refused, because the bridge would deliver to the gone planner" || nope "rc $rc: $OUT"
case "$OUT" in *"HERDR_PLANNER_NAME=planner-1 tandem-unpair --force"*) ok "and names the command that clears it" ;; *) nope "no remedy: $OUT" ;; esac

printf '\n== a relay with no pair.json ==\n'
rm -f "$MB/pair.json"
OUT=$(check planner-2 w1:p8 planner); rc=$?
[ "$rc" -ne 0 ] && case "$OUT" in *"'planner'"*) true ;; *) false ;; esac \
  && ok "its owner is the checkout's derived name, 'planner'" || nope "rc $rc: $OUT"

printf "\n== the bridge's own --planner names the owner ==\n"
# With no pair.json, the bridge still knows.
"$TMP/bin/tandem-bridge" 300 --mailbox "$MB" --planner planner-3 >/dev/null 2>&1 & BRIDGE2=$!
printf '%s\n' "$BRIDGE2" > "$MB/bridge.pid"
OUT=$(check planner-x w1:p8 planner-3); rc=$?
pkill -P "$BRIDGE2" 2>/dev/null; kill "$BRIDGE2" 2>/dev/null
[ "$rc" -ne 0 ] && case "$OUT" in *"'planner-3' in pane w1:p7"*) true ;; *) false ;; esac \
  && ok "the refusal names the planner the bridge delivers to" || nope "rc $rc: $OUT"

printf '\ntests/test-pair-one-per-checkout: passed %d, failed %d\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

#!/usr/bin/env bash
# tandem-bridge's deliver() branches, through --once, which processes the
# queue and exits.
#
# Hermetic: herdr and tandem are controllable stubs. The tandem stubs pass
# relay read, state active-run to the real binary. jq fails:
# the bridge asks tandem, so any jq call is a bug. The fixtures use real jq.
set -uo pipefail
BRIDGE="${1:-$(cd "$(dirname "$0")/.." && pwd)/bin/tandem-bridge}"
[ -x "$BRIDGE" ] || { echo "not executable: $BRIDGE" >&2; exit 2; }

PASS=0; FAIL=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }
chk()  { case "$OUT" in *"$2"*) ok "$1" ;; *) nope "$1 (missing: $2)" ;; esac; }
nchk() { case "$OUT" in *"$2"*) nope "$1 (unexpected: $2)" ;; *) ok "$1" ;; esac; }

TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
mkdir -p "$TMP/bin"
printf '#!/bin/sh\necho "jq was called: $*" >&2\nexit 99\n' > "$TMP/bin/jq"
chmod +x "$TMP/bin/jq"
(cd "$(dirname "$0")/.." && go build -o "$TMP/tandem-real" ./cmd/tandem) || { echo "could not build tandem" >&2; exit 2; }
export TANDEM_REAL="$TMP/tandem-real"

# herdr stub: agent get reports HERDR_GET_STATUS (default idle); agent wait
# exits HERDR_WAIT_EXIT; agent prompt logs its message to HERDR_PROMPT_LOG and,
# with HERDR_PROMPT_FAIL=1, writes an error code to stderr and exits 1.
# HERDR_ARGV_LOG records every argv.
cat > "$TMP/bin/herdr" <<'STUB'
#!/usr/bin/env bash
[ -n "${HERDR_ARGV_LOG:-}" ] && printf '%s\n' "$*" >> "$HERDR_ARGV_LOG"
case "$1 $2" in
  "agent get")
    printf '{"result":{"agent":{"agent_status":"%s"}}}\n' "${HERDR_GET_STATUS:-idle}"
    exit 0 ;;
  "agent wait")
    printf '%s\n' "wait $PLANNER_NAME" >> "${HERDR_WAIT_LOG:-/dev/null}"
    exit "${HERDR_WAIT_EXIT:-0}" ;;
  "agent prompt")
    printf '%s' "$4" > "${HERDR_PROMPT_LOG:-/dev/null}"
    if [ "${HERDR_PROMPT_FAIL:-0}" = 1 ]; then
      printf '{"error":{"code":"%s"}}' "${HERDR_PROMPT_ERROR_CODE:-unknown}" >&2
      exit 1
    fi
    exit 0 ;;
esac
exit 0
STUB
chmod +x "$TMP/bin/herdr"

# The bridge delivers through `tandem agent prompt --as`, so this stub logs
# argv, records the prompt text and, with HERDR_PROMPT_FAIL=1, fails as tandem
# does. The build-task section replaces it later.
cat > "$TMP/bin/tandem" <<'STUB'
#!/usr/bin/env bash
[ -n "${HERDR_ARGV_LOG:-}" ] && printf '%s\n' "$*" >> "$HERDR_ARGV_LOG"
case "$1 $2" in
  "relay read") [ "${RELAY_READ_FAIL:-0}" = 1 ] && exit 1; exec "$TANDEM_REAL" "$@" ;;
  "state active-run") exec "$TANDEM_REAL" "$@" ;;
esac
case "$1 $2" in
  "agent prompt")
    printf '%s' "$4" > "${HERDR_PROMPT_LOG:-/dev/null}"
    if [ "${HERDR_PROMPT_FAIL:-0}" = 1 ]; then
      printf '%s\t%s\n' "${HERDR_PROMPT_ERROR_CODE:-unknown}" "${HERDR_PROMPT_ERROR_MSG:-simulated failure}" >&2
      exit 10
    fi
    exit 0 ;;
esac
exit 0
STUB
chmod +x "$TMP/bin/tandem"

MB="$TMP/mailbox"
PROMPT_LOG="$TMP/prompt.log"
WAIT_LOG="$TMP/wait.log"

run_bridge() {
  mkdir -p "$MB"
  : > "$PROMPT_LOG"; : > "$WAIT_LOG"
  OUT=$(PATH="$TMP/bin:$PATH" HERDR_ENV=1 HERDR_GET_STATUS="${HERDR_GET_STATUS:-idle}" \
    HERDR_WAIT_EXIT="${HERDR_WAIT_EXIT:-0}" HERDR_PROMPT_FAIL="${HERDR_PROMPT_FAIL:-0}" \
    HERDR_PROMPT_ERROR_CODE="${HERDR_PROMPT_ERROR_CODE:-}" RELAY_READ_FAIL="${RELAY_READ_FAIL:-0}" \
    HERDR_PROMPT_LOG="$PROMPT_LOG" HERDR_WAIT_LOG="$WAIT_LOG" \
    "$BRIDGE" --mailbox "$MB" --planner planner --once 2>&1); RC=$?
}
reset_mb() { rm -rf "$MB"; }

echo "--- a well-formed question is delivered, no answer file written on success ---"
reset_mb; mkdir -p "$MB/q"
jq -n --arg id q1 --arg from builder --arg q "what now?" \
  '{id:$id, from:$from, question:$q, created:(now|todate)}' > "$MB/q/q1.json"
run_bridge
[ "$RC" -eq 0 ] && ok "--once exits 0" || nope "--once exits 0 (got $RC)"
chk "it logs the delivery" "q1 delivered"
[ -f "$MB/q/q1.json.taken" ] && ok "the question file was claimed (renamed .taken)" \
                              || nope "the question file was not claimed"
[ ! -e "$MB/q/q1.json" ] && ok "the original queue file is gone" \
                          || nope "the original queue file is still present"
[ ! -e "$MB/a/q1.md" ] && ok "no answer file is written on a successful question delivery" \
                        || nope "an answer file was written on success (should be the planner's job)"
grep -q "^\[from builder\] what now?" "$PROMPT_LOG" && ok "the prompt names the sender and the question" \
                                                     || nope "the prompt text is wrong: $(cat "$PROMPT_LOG")"
grep -q "$MB/a/q1.md" "$PROMPT_LOG" && ok "the prompt tells the planner exactly where to write its answer" \
                                     || nope "the prompt does not name the answer path"
grep -q "builder cannot see this conversation, so put" "$PROMPT_LOG" && ok "the prompt says the asker cannot see the conversation" \
  || nope "the prompt lost its self-contained line: $(cat "$PROMPT_LOG")"
grep -q '—' "$PROMPT_LOG" && nope "the prompt has an em dash" || ok "the prompt has no em dash"

echo "--- a question that fails delivery gets a fail_answer, not silence ---"
reset_mb; mkdir -p "$MB/q"
jq -n --arg id q2 --arg from builder --arg q "still there?" \
  '{id:$id, from:$from, question:$q, created:(now|todate)}' > "$MB/q/q2.json"
HERDR_PROMPT_FAIL=1 HERDR_PROMPT_ERROR_CODE=agent_blocked run_bridge
chk "it logs the failure with the herdr error code" "q2 delivery FAILED (agent_blocked)"
[ -f "$MB/a/q2.md" ] && ok "a fail-answer file was written" || nope "no fail-answer file was written"
grep -q "not delivered" "$MB/a/q2.md" 2>/dev/null && ok "the fail-answer explains delivery failed" \
                                                    || nope "fail-answer content is wrong"
grep -q "agent_blocked" "$MB/a/q2.md" 2>/dev/null && ok "the fail-answer names the herdr error code" \
                                                    || nope "fail-answer does not name the error code"
grep -q "tandem agent prompt failed" "$MB/a/q2.md" 2>/dev/null && ok "the fail-answer names the command that failed" \
                                                                || nope "fail-answer does not say tandem agent prompt failed"

echo "--- a blocked planner is waited out inside --as, not by the bridge ---"
reset_mb; mkdir -p "$MB/q"
jq -n --arg id q3 --arg from builder --arg q "ping" \
  '{id:$id, from:$from, question:$q, created:(now|todate)}' > "$MB/q/q3.json"
ARGV_LOG="$TMP/argv.log"; : > "$ARGV_LOG"
HERDR_ARGV_LOG="$ARGV_LOG" run_bridge
chk "it still delivers" "q3 delivered"
grep -q -e "--as question --wait-unblocked 120000" "$ARGV_LOG" \
  && ok "it asks tandem to wait out a dialog for the prompt timeout" \
  || nope "the prompt did not carry --wait-unblocked: $(cat "$ARGV_LOG")"

echo "--- a handback with an explicit message is passed through verbatim ---"
reset_mb; mkdir -p "$MB/q"
jq -n --arg id h1 --arg from builder --arg msg "[handback from builder] custom text, verbatim" \
  '{id:$id, kind:"handback", from:$from, question:"unused", message:$msg}' > "$MB/q/h1.json"
run_bridge
chk "it logs the handback delivery" "h1 delivered"
grep -qF "custom text, verbatim" "$PROMPT_LOG" && ok "the explicit message is passed through unchanged" \
                                                 || nope "the bridge rewrote an explicit handback message"
[ ! -e "$MB/a/h1.md" ] && ok "a successful handback writes no answer file (nobody polls for one)" \
                        || nope "a handback should never write an answer file"

echo "--- a handback with no message is skipped, not composed by the bridge ---"
reset_mb; mkdir -p "$MB/q"
jq -n --arg id h2 --arg from builder --arg rp "report.md" \
  '{id:$id, kind:"handback", from:$from, question:"unused", report_path:$rp}' > "$MB/q/h2.json"
run_bridge
chk "it logs the handback as malformed" "malformed handback file"
[ ! -s "$PROMPT_LOG" ] && ok "nothing is typed for a handback with no message" \
                       || nope "the bridge typed its own text for a handback with no message"

echo "--- a question over several lines arrives whole ---"
reset_mb; mkdir -p "$MB/q"
jq -n --arg id q9 --arg from builder --arg q $'first line\nsecond line' \
  '{id:$id, from:$from, question:$q}' > "$MB/q/q9.json"
run_bridge
grep -qF "second line" "$PROMPT_LOG" && grep -qF "[from builder] first line" "$PROMPT_LOG" \
  && ok "both lines of the question reach the planner" \
  || nope "a question over several lines was cut short"

echo "--- a handback that fails delivery leaves an UNDELIVERED marker, not a fail_answer ---"
reset_mb; mkdir -p "$MB/q"
jq -n --arg id h3 --arg from builder \
  '{id:$id, kind:"handback", from:$from, question:"unused", message:"hi"}' > "$MB/q/h3.json"
HERDR_PROMPT_FAIL=1 HERDR_PROMPT_ERROR_CODE=transport run_bridge
[ -f "$MB/reports/h3.UNDELIVERED" ] && ok "an UNDELIVERED marker was written" \
                                     || nope "no UNDELIVERED marker was written for a failed handback"
grep -q "transport" "$MB/reports/h3.UNDELIVERED" 2>/dev/null && ok "the marker names the herdr error code" \
                                                              || nope "marker does not name the error code"
grep -q "tandem agent prompt failed" "$MB/reports/h3.UNDELIVERED" 2>/dev/null && ok "the marker names the command that failed" \
                                                                             || nope "marker does not say tandem agent prompt failed"
[ ! -e "$MB/a/h3.md" ] && ok "a failed handback still writes no answer file" \
                        || nope "a failed handback wrote an answer file (should be an UNDELIVERED marker instead)"

echo "--- a verdict is passed through verbatim, like a handback ---"
# tandem-verdict queues one when the reviewer cannot reach the socket.
reset_mb; mkdir -p "$MB/q"
jq -n --arg id v1 --arg from builder --arg msg "tandem-verdict: the builder reviewed p.md and returned: clean, verbatim" \
  '{id:$id, kind:"verdict", from:$from, question:"verdict", message:$msg}' > "$MB/q/v1.json"
run_bridge
chk "it logs the verdict delivery" "v1 delivered"
grep -qF "returned: clean, verbatim" "$PROMPT_LOG" && ok "the verdict message is passed through unchanged" \
                                                    || nope "the bridge rewrote a verdict message"
[ ! -e "$MB/a/v1.md" ] && ok "a verdict writes no answer file (nobody polls for one)" \
                        || nope "a verdict should never write an answer file"

echo "--- a verdict with no message is skipped, not composed by the bridge ---"
reset_mb; mkdir -p "$MB/q"
jq -n --arg id v3 --arg from builder --arg rp "reviews/v3.md" \
  '{id:$id, kind:"verdict", from:$from, question:"verdict", review_path:$rp}' > "$MB/q/v3.json"
run_bridge
chk "it logs the verdict as malformed" "malformed verdict file"
[ ! -s "$PROMPT_LOG" ] && ok "nothing is typed for a verdict with no message" \
                       || nope "the bridge typed its own text for a verdict with no message"

echo "--- a verdict that fails delivery leaves its marker beside the review ---"
# Not in reports/, which holds handbacks.
reset_mb; mkdir -p "$MB/q"
jq -n --arg id v2 --arg from builder \
  '{id:$id, kind:"verdict", from:$from, question:"verdict", message:"hi"}' > "$MB/q/v2.json"
HERDR_PROMPT_FAIL=1 HERDR_PROMPT_ERROR_CODE=transport run_bridge
[ -f "$MB/reviews/v2.UNDELIVERED" ] && ok "an UNDELIVERED marker was written beside the review" \
                                     || nope "no UNDELIVERED marker beside the review for a failed verdict"
grep -q "transport" "$MB/reviews/v2.UNDELIVERED" 2>/dev/null && ok "the verdict marker names the herdr error code" \
                                                              || nope "the verdict marker does not name the error code"
[ ! -e "$MB/reports/v2.UNDELIVERED" ] && ok "nothing is written among the handback reports" \
                                      || nope "a verdict marker was written among the handback reports"
[ ! -e "$MB/a/v2.md" ] && ok "a failed verdict writes no answer file" \
                        || nope "a failed verdict wrote an answer file"

echo "--- a verdict refused at a subagent's screen says so in its marker ---"
reset_mb; mkdir -p "$MB/q"
jq -n --arg id v4 --arg from builder \
  '{id:$id, kind:"verdict", from:$from, question:"verdict", message:"hi"}' > "$MB/q/v4.json"
HERDR_PROMPT_FAIL=1 HERDR_PROMPT_ERROR_CODE=agent_input_elsewhere \
  HERDR_PROMPT_ERROR_MSG="planner's pane shows the screen of subagent \"Fix it\"; nothing was typed" run_bridge
chk "the log names the code" "v4 delivery FAILED (agent_input_elsewhere)"
chk "the log says what the pane shows" 'subagent "Fix it"; nothing was typed'
grep -qF 'subagent "Fix it"; nothing was typed' "$MB/reviews/v4.UNDELIVERED" 2>/dev/null \
  && ok "the marker says what the pane shows" \
  || nope "the marker does not say what the pane shows"

echo "--- a malformed question file (no question field) is skipped, not crashed on ---"
reset_mb; mkdir -p "$MB/q"
jq -n --arg id bad1 '{id:$id}' > "$MB/q/bad1.json"
run_bridge
[ "$RC" -eq 0 ] && ok "a malformed file does not crash the bridge" || nope "bridge exited $RC on a malformed file"
chk "it logs the file as malformed" "malformed question file $MB/q/bad1.json.taken, skipped"
nchk "it does not claim to have delivered a malformed file" "delivered"

echo "--- a queued file that is not a JSON object is skipped, and the next is delivered ---"
reset_mb; mkdir -p "$MB/q"
printf '[1,2]\n' > "$MB/q/arr.json"
jq -n '{id:"ok1", from:"builder", question:"after the array"}' > "$MB/q/ok1.json"
run_bridge
[ "$RC" -eq 0 ] && ok "an array does not stop the bridge" || nope "bridge exited $RC on an array"
chk "it logs the array as malformed" "malformed question file $MB/q/arr.json.taken"
chk "the next question is still delivered" "ok1 delivered"

echo "--- a relay read that fails is logged as malformed, and the bridge goes on ---"
reset_mb; mkdir -p "$MB/q"
jq -n '{id:"rf1", from:"builder", question:"unread"}' > "$MB/q/rf1.json"
RELAY_READ_FAIL=1 run_bridge
[ "$RC" -eq 0 ] && ok "a failing tandem does not stop the bridge" || nope "bridge exited $RC when relay read failed"
chk "it logs the file as malformed" "malformed question file"
nchk "it delivers nothing" "delivered"

echo "--- a question holding a tab, a newline and a unit separator arrives whole ---"
reset_mb; mkdir -p "$MB/q"
Q=$'col1\tcol2\nline2 \x1f after'
jq -n --arg q "$Q" '{id:"sep1", from:"builder", question:$q}' > "$MB/q/sep1.json"
run_bridge
chk "it delivers the question" "sep1 delivered"
[ "$(head -c "$(( ${#Q} + 15 ))" "$PROMPT_LOG")" = "[from builder] $Q" ] && ok "the question reaches the planner unchanged" \
  || nope "the question changed: $(head -3 "$PROMPT_LOG")"

echo "--- a question with no sender is from the builder ---"
reset_mb; mkdir -p "$MB/q"
jq -n '{id:"nf1", from:null, question:"who?"}' > "$MB/q/nf1.json"
run_bridge
grep -q '^\[from builder\] who?' "$PROMPT_LOG" && ok "a null sender reads as builder" || nope "sender: $(head -1 "$PROMPT_LOG")"

echo "--- each bridge keeps its own fields file, and leaves none behind ---"
reset_mb; mkdir -p "$MB/q"
# A file left by a bridge that is gone, and one held by a live process.
sleep 30 & LIVE=$!
: > "$MB/.fields.99999999"; : > "$MB/.fields.$LIVE"
jq -n '{id:"ff1", from:"builder", question:"fields"}' > "$MB/q/ff1.json"
run_bridge
chk "it delivers the question" "ff1 delivered"
[ ! -e "$MB/.fields" ] && ok "no fields file is shared between bridges" || nope "a shared $MB/.fields exists"
[ ! -e "$MB/.fields.99999999" ] && ok "a dead bridge's fields file is swept at start" || nope "a dead bridge's fields file was left"
[ -e "$MB/.fields.$LIVE" ] && ok "a live bridge's fields file is left alone" || nope "a live bridge's fields file was removed"
[ "$(ls -a "$MB" | grep -c '^\.fields')" -eq 1 ] && ok "the bridge removes its own fields file on exit" || nope "fields files left: $(ls -a "$MB" | grep '^\.fields' | tr '\n' ' ')"
kill "$LIVE" 2>/dev/null; wait "$LIVE" 2>/dev/null

echo "--- --once with an empty mailbox does nothing and exits cleanly ---"
reset_mb; mkdir -p "$MB/q"
run_bridge
[ "$RC" -eq 0 ] && ok "an empty mailbox still exits 0" || nope "exited $RC on an empty mailbox"
chk "it reports oneshot completion" "oneshot done"

echo "--- --once processes everything queued in a single pass ---"
reset_mb; mkdir -p "$MB/q"
jq -n --arg id m1 --arg q "one" '{id:$id, from:"builder", question:$q}' > "$MB/q/m1.json"
jq -n --arg id m2 --arg q "two" '{id:$id, from:"builder", question:$q}' > "$MB/q/m2.json"
run_bridge
chk "it delivers the first queued question" "m1 delivered"
chk "it delivers the second queued question" "m2 delivered"

echo "--- --planner names a custom target, not just the default 'planner' ---"
reset_mb; mkdir -p "$MB/q"
jq -n --arg id p1 --arg from builder --arg q "who are you talking to?" \
  '{id:$id, from:$from, question:$q}' > "$MB/q/p1.json"
ARGV_LOG="$TMP/argv.log"; : > "$ARGV_LOG"
OUT=$(PATH="$TMP/bin:$PATH" HERDR_ENV=1 HERDR_PROMPT_LOG="$PROMPT_LOG" HERDR_WAIT_LOG="$WAIT_LOG" \
  HERDR_ARGV_LOG="$ARGV_LOG" \
  "$BRIDGE" --mailbox "$MB" --planner custom-target --once 2>&1); RC=$?
[ "$RC" -eq 0 ] && ok "--planner custom-target still exits 0" || nope "--planner exits 0 (got $RC)"
chk "it delivers the question" "p1 delivered"
grep -q "^agent prompt custom-target " "$ARGV_LOG" && ok "it prompts the named target, not the default" \
                                                     || nope "agent prompt was not called with --planner's target"

TLOG="$TMP/tandem.log"; MSGFILE="$TMP/msg.md"

echo "--- a durable run: the loop waits on events, and a gone agent gets an observing tick ---"
# `tandem agent watch` is stubbed: WATCH_RC=27 means an agent is gone,
# anything else is a timeout. The bridge runs in the background and is killed
# after its turns.
cat > "$TMP/bin/tandem" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$TANDEM_LOG"
case "$1 $2" in
  "relay read") [ "${RELAY_READ_FAIL:-0}" = 1 ] && exit 1; exec "$TANDEM_REAL" "$@" ;;
  "state active-run") exec "$TANDEM_REAL" "$@" ;;
esac
case "$1 $2" in
  "agent watch")
    [ "${WATCH_RC:-0}" = 27 ] && { printf 'agent_exited\tbuilder\n'; exit 27; }
    [ -n "${WATCH_OUT:-}" ] && { printf '%s\n' "$WATCH_OUT"; exit 0; }
    sleep 0.2; printf 'timeout\n'; exit 0 ;;
esac
exit 0
STUB
RUN1=0123456789abcdef0123456789abcdef
durable_loop() { # durable_loop SECONDS [norun]
  reset_mb; mkdir -p "$MB/durable/$RUN1/q"
  [ "${2:-}" = norun ] || printf '{"engine_version":2,"protocol_version":2,"run_id":"%s"}\n' "$RUN1" > "$MB/active-run.json"
  : > "$TLOG"
  PATH="$TMP/bin:$PATH" HERDR_ENV=1 HERDR_PROMPT_LOG="$PROMPT_LOG" TANDEM_LOG="$TLOG" WATCH_RC="${WATCH_RC:-0}" WATCH_OUT="${WATCH_OUT:-}" \
    HERDR_BRIDGE_POLL_SECONDS=1 HERDR_BRIDGE_BACKSTOP_SECONDS=7 "$BRIDGE" --mailbox "$MB" --planner planner >/dev/null 2>&1 &
  local pid=$!
  sleep "$1"; kill "$pid" 2>/dev/null; wait "$pid" 2>/dev/null
  return 0
}
WATCH_RC=0; durable_loop 2
grep -q -- "^agent watch --mailbox $MB --timeout 7000 --root $TMP\$" "$TLOG" \
  && ok "the loop waits with agent watch on the mailbox and the run's root, for the backstop" || nope "no agent watch ($(cat "$TLOG"))"
grep -q -- "observe-agents" "$TLOG" && nope "a quiet pass asked for an observing tick" || ok "a quiet pass runs the plain tick only"
WATCH_RC=27; durable_loop 2
grep -q -- "^build tick --root $TMP --observe-agents" "$TLOG" \
  && ok "a gone agent gets a tick that observes the agents" || nope "no observing tick ($(cat "$TLOG"))"
WATCH_RC=0; durable_loop 2 norun
grep -q -- "^agent watch --mailbox $MB --timeout 7000\$" "$TLOG" \
  && ok "with no durable run the loop waits on the mailbox alone, not on a sleep" || nope "no mailbox-only watch ($(cat "$TLOG"))"
grep -q -- "^build tick" "$TLOG" && nope "a pass with no durable run ticked" || ok "and runs no tick"
# A wake the pass cannot act on must be paced, or the loop spins.
watches() { grep -c '^agent watch' "$TLOG" 2>/dev/null || true; }
WATCH_OUT="$(printf 'queued\t%s/q/stuck.json' "$MB")"; durable_loop 3 norun
[ "$(watches)" -le 5 ] && ok "a queued wake that names the same file again is paced ($(watches) watches in 3 s)" \
  || nope "the same queued wake looped: $(watches) watches in 3 s"
WATCH_OUT="$(printf 'mailbox_error\tevent queue overflow')"; durable_loop 3 norun
[ "$(watches)" -le 5 ] && ok "a watcher error is paced ($(watches) watches in 3 s)" \
  || nope "a watcher error looped: $(watches) watches in 3 s"
WATCH_OUT=""

echo "--- an old task message is set aside and the bridge goes on ---"
reset_mb; mkdir -p "$MB/q"
printf '{"id":"t1","kind":"task","from":"builder","question":"task","task":1}\n' > "$MB/q/t1.json"
OUT=$(PATH="$TMP/bin:$PATH" HERDR_ENV=1 HERDR_PROMPT_LOG="$PROMPT_LOG" TANDEM_LOG="$TLOG" "$BRIDGE" --mailbox "$MB" --planner planner --once 2>&1); RC=$?
[ "$RC" -eq 0 ] && ok "the bridge keeps running" || nope "the bridge stopped (rc $RC): $OUT"
[ -f "$MB/quarantine-v1/t1.json" ] && grep -q 'legacy is gone' "$MB/quarantine-v1/t1.json.reason" 2>/dev/null \
  && ok "the message is set aside with its reason" || nope "the message was not set aside: $OUT"

echo "--- --mailbox is required ---"
OUT=$(PATH="$TMP/bin:$PATH" HERDR_ENV=1 "$BRIDGE" --planner planner --once 2>&1); RC=$?
[ "$RC" -ne 0 ] && ok "refuses without --mailbox" || nope "should refuse without --mailbox"
chk "it names --mailbox as required" "--mailbox is required"

echo "--- refuses outside a Herdr pane ---"
OUT=$(env -u HERDR_ENV PATH="$TMP/bin:$PATH" "$BRIDGE" --mailbox "$MB" --once 2>&1); RC=$?
[ "$RC" -ne 0 ] && ok "refuses without HERDR_ENV" || nope "should refuse without HERDR_ENV"
chk "it names HERDR_ENV as the reason" "must run inside a Herdr pane (HERDR_ENV != 1), because it needs the Herdr socket."

printf '\ntandem-bridge: passed %d, failed %d\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

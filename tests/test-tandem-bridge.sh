#!/usr/bin/env bash
# tandem-bridge's deliver() branches, through --once, which processes the
# queue and exits.
#
# Hermetic: herdr and tandem are controllable stubs. The tandem stubs pass
# relay read, state active-run and build roles to the real binary. jq fails:
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
  "state active-run"|"build roles") exec "$TANDEM_REAL" "$@" ;;
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
chk "it logs the file as malformed" "malformed question file"
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

# --- a build task handed back ---
# build advance judges the task; the bridge sends the review, the findings,
# the next task, or tells the planner. ADVANCE_OUT is what advance prints,
# HANDOFF_RC what handoff exits with, BUILDER_STATUS the delivery proof.
cat > "$TMP/bin/tandem" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$TANDEM_LOG"
case "$1 $2" in
  "relay read") [ "${RELAY_READ_FAIL:-0}" = 1 ] && exit 1; exec "$TANDEM_REAL" "$@" ;;
  "state active-run"|"build roles") exec "$TANDEM_REAL" "$@" ;;
esac
case "$1 $2" in
  "build advance") printf '%s\n' "$ADVANCE_OUT"; [ -n "${ADVANCE_ERR:-}" ] && printf '%s\n' "$ADVANCE_ERR" >&2; exit "${ADVANCE_RC:-0}" ;;
  "build stop")    printf 'stopped handoff R9 %s\n' "$MSGFILE"; exit 0 ;;
  "agent prompt")
    # A prompt to the planner is the bridge's delivery; any other target is logged
    # to BUILDER_TARGET_LOG.
    if [ "$3" = planner ]; then printf '%s' "$4" > "$HERDR_PROMPT_LOG"; exit 0; fi
    printf '%s\n' "$3" > "$BUILDER_TARGET_LOG"; printf '%s' "$4" > "$BUILDER_PROMPT_LOG"
    # --as: BUILDER_TURN=ends is a turn that ended at once; an idle BUILDER_STATUS
    # is a prompt that never started.
    case "$*" in *--as*)
      if [ "${BUILDER_TURN:-}" = ends ]; then
        printf "agent_turn_failed\t%s's turn ended as soon as it started\nThere's an issue with the selected model (sonnet-5.5).\n" "$3" >&2
        exit 26
      fi
      [ "${BUILDER_STATUS:-working}" = idle ] && { printf 'agent_never_started\tthe prompt never started\n' >&2; exit 25; }
      printf 'started\t%s\n' "${BUILDER_STATUS:-working}" ;;
    esac
    exit 0 ;;
  "agent status")  printf '%s\n' "${BUILDER_STATUS:-working}"; exit 0 ;;
esac
exit 0
STUB
cat > "$TMP/bin/tandem-handoff" <<'STUB'
#!/usr/bin/env bash
printf 'handoff %s builder=%s\n' "$*" "$HERDR_BUILDER_NAME" >> "$TANDEM_LOG"
[ "${HANDOFF_RC:-0}" = 0 ] || { echo "tandem-handoff: this plan has not cleared review." >&2; exit "$HANDOFF_RC"; }
exit 0
STUB
chmod +x "$TMP/bin/tandem" "$TMP/bin/tandem-handoff"
TLOG="$TMP/tandem.log"; MSGFILE="$TMP/msg.md"
BUILDER_TARGET_LOG="$TMP/builder-target.log"; BUILDER_PROMPT_LOG="$TMP/builder-prompt.log"
EVENT_CREATED="2026-09-30T14:00:00Z"
printf 'the build message\n' > "$MSGFILE"
queue_task() {
  reset_mb; mkdir -p "$MB/q" "$MB/build"
  printf '{"plan":"/repo/p.md","builder":"bob","reviewer":"rita"}\n' > "$MB/build/build.json"
  jq -n --argjson stuck "${1:-false}" --arg created "$EVENT_CREATED" \
    '{id:"t1", kind:"task", from:"bob", question:"task", task:2,
     stuck:$stuck, review:false, verdict:"", report_path:"/r/task-2.report.md", created:$created}' > "$MB/q/t1.json"
  : > "$TLOG"
  : > "$PROMPT_LOG"; : > "$BUILDER_TARGET_LOG"; : > "$BUILDER_PROMPT_LOG"
}
run_task_bridge() {
  OUT=$(PATH="$TMP/bin:$PATH" HERDR_ENV=1 HERDR_PROMPT_LOG="$PROMPT_LOG" TANDEM_LOG="$TLOG" \
        MSGFILE="$MSGFILE" ADVANCE_OUT="$ADVANCE_OUT" ADVANCE_RC="${ADVANCE_RC:-0}" ADVANCE_ERR="${ADVANCE_ERR:-}" HANDOFF_RC="${HANDOFF_RC:-0}" \
        BUILDER_STATUS="${BUILDER_STATUS:-working}" BUILDER_TURN="${BUILDER_TURN:-}" BUILDER_TARGET_LOG="$BUILDER_TARGET_LOG" BUILDER_PROMPT_LOG="$BUILDER_PROMPT_LOG" \
        "$BRIDGE" --mailbox "$MB" --planner planner --once 2>&1); RC=$?
}

echo "--- a task that passes sends the next one, and wakes nobody ---"
queue_task; : > "$PROMPT_LOG"; ADVANCE_OUT="next 2"; HANDOFF_RC=0
run_task_bridge
grep -q -- "build advance --root $TMP --task 2" "$TLOG" && grep -q -- "--report /r/task-2.report.md" "$TLOG" \
  && ok "advance is told the root, the task and the report" || nope "advance is told the root, the task and the report ($(cat "$TLOG"))"
grep -q -- "--event t1" "$TLOG" && grep -q -- "--created $EVENT_CREATED" "$TLOG" \
  && ok "advance receives the queue event ID and creation time" || nope "advance lost the queue event metadata ($(cat "$TLOG"))"
grep -q -- "handoff /repo/p.md --continue builder=bob" "$TLOG" \
  && ok "the next task is sent with handoff --continue, to the build's builder" || nope "the next task is sent ($(cat "$TLOG"))"
[ -s "$PROMPT_LOG" ] && nope "the planner was woken for a task that went well" || ok "the planner is not woken"

echo "--- a review is typed into the reviewer pane, never the builder, and proved delivered ---"
queue_task
printf '{"plan":"/repo/p.md","builder":"builder","reviewer":"reviewer"}\n' > "$MB/build/build.json"
ADVANCE_OUT="review 1 $MSGFILE"
run_task_bridge
[ "$(cat "$BUILDER_TARGET_LOG")" = reviewer ] && ok "the review targets the build's reviewer" || nope "the review targets $(cat "$BUILDER_TARGET_LOG"), not the reviewer"
grep -q -- "^agent prompt builder" "$TLOG" && nope "the review was typed into the builder" || ok "nothing is typed into the builder"
[ "$(cat "$BUILDER_PROMPT_LOG")" = "the build message" ] && ok "the reviewer receives the review file's contents" || nope "the review message was not delivered"
grep -q -- "^agent prompt reviewer .*--as review$" "$TLOG" && ok "delivery is confirmed by agent prompt --as review" || nope "the review was typed without --as review"
grep -q -- "build review-sent --root $TMP --task 1" "$TLOG" && ok "delivery is recorded on task 1" || nope "review-sent was not recorded"
grep -q "^handoff" "$TLOG" && nope "a review reset the conversation through handoff" || ok "a review does not call handoff"
[ -s "$PROMPT_LOG" ] && nope "a delivered review woke the planner" || ok "a delivered review wakes nobody"

echo "--- a build that records no reviewer pane is stopped by advance, and the planner told ---"
# build advance decides the stop; the bridge only relays it.
queue_task; : > "$PROMPT_LOG"
printf '{"plan":"/repo/p.md","builder":"bob"}\n' > "$MB/build/build.json"
ADVANCE_OUT="stopped no_reviewer R1 $MSGFILE"
run_task_bridge
[ -s "$BUILDER_TARGET_LOG" ] && nope "a review was typed into $(cat "$BUILDER_TARGET_LOG")" || ok "nothing is typed into any pane"
grep -q -- "^build stop" "$TLOG" && nope "the bridge stopped the build itself ($(cat "$TLOG"))" || ok "the bridge records no stop of its own"
[ "$(cat "$PROMPT_LOG")" = "the build message" ] && ok "the planner gets the build's message" || nope "the planner was not told ($(cat "$PROMPT_LOG"))"

echo "--- a review that leaves the reviewer idle stops its task and tells the planner ---"
queue_task; ADVANCE_OUT="review 1 $MSGFILE"
BUILDER_STATUS=idle run_task_bridge
grep -q -- "build stop --root $TMP --task 1 --reason handoff" "$TLOG" \
  && ok "undelivered review stops task 1 with reason handoff" || nope "undelivered review did not stop the right task"
grep -q -- "not delivered to rita" "$TLOG" && ok "the stop explains the failed review delivery" || nope "the stop lost the delivery reason"
[ "$(cat "$PROMPT_LOG")" = "the build message" ] && ok "the planner receives the stop message" || nope "the planner did not receive the stop message"
grep -q -- "build review-sent" "$TLOG" && nope "idle was recorded as delivered" || ok "idle is not recorded as delivered"
grep -q "^handoff" "$TLOG" && nope "a failed review called handoff" || ok "a failed review does not call handoff"

echo "--- a review whose turn ends at once stops its task and shows the pane ---"
queue_task; ADVANCE_OUT="review 1 $MSGFILE"
BUILDER_TURN=ends run_task_bridge
grep -q -- "build stop --root $TMP --task 1 --reason handoff" "$TLOG" \
  && ok "the review stops task 1 with reason handoff" || nope "a review whose turn ended at once did not stop task 1"
grep -q -- "issue with the selected model" "$TLOG" && ok "the stop carries what the pane shows" || nope "the stop lost what the pane shows"
grep -q -- "build review-sent" "$TLOG" && nope "a turn that ended at once was recorded as delivered" || ok "it is not recorded as delivered"

echo "--- the reviewer's verdict reaches advance as a review, with its pane ---"
queue_task
jq -n --arg created "$EVENT_CREATED" \
  '{id:"t1", kind:"task", from:"rita", question:"task", task:2, stuck:false, review:true,
    verdict:"changes", report_path:"/r/task-2.review.md", created:$created}' > "$MB/q/t1.json"
ADVANCE_OUT="fix 2 $MSGFILE"
run_task_bridge
grep -q -- "build advance --root $TMP --task 2 .*--review --verdict changes --from rita" "$TLOG" \
  && ok "advance hears --review, the verdict and the pane" || nope "advance lost the review ($(cat "$TLOG"))"
grep -q -- "--report /r/task-2.review.md" "$TLOG" && ok "and the findings report" || nope "the findings report was lost"

echo "--- findings go to the builder's conversation, not a fresh one ---"
[ "$(cat "$BUILDER_TARGET_LOG")" = bob ] && ok "the fix targets the build's builder" || nope "the fix targets $(cat "$BUILDER_TARGET_LOG")"
[ "$(cat "$BUILDER_PROMPT_LOG")" = "the build message" ] && ok "the builder receives the fix prompt" || nope "the fix prompt was not delivered"
grep -q -- "^agent prompt bob .*--as task$" "$TLOG" && ok "delivery is confirmed by agent prompt --as task" || nope "the fix was typed without --as task"
grep -q "^handoff" "$TLOG" && nope "a fix reset the builder through handoff" || ok "a fix keeps the builder's conversation"
grep -q -- "build review-sent" "$TLOG" && nope "a fix was recorded as a review" || ok "a fix is not recorded as a review"
[ -s "$PROMPT_LOG" ] && nope "a delivered fix woke the planner" || ok "a delivered fix wakes nobody"

echo "--- a fix the builder never starts stops its task and tells the planner ---"
queue_task; ADVANCE_OUT="fix 2 $MSGFILE"
BUILDER_STATUS=idle run_task_bridge
grep -q -- "build stop --root $TMP --task 2 --reason handoff" "$TLOG" \
  && ok "an undelivered fix stops task 2" || nope "an undelivered fix did not stop task 2 ($(cat "$TLOG"))"
grep -q -- "findings for task 2 were not delivered to bob" "$TLOG" && ok "the stop says why" || nope "the stop lost the reason"
[ "$(cat "$PROMPT_LOG")" = "the build message" ] && ok "the planner is told" || nope "the planner was not told"

echo "--- a refused review handback is logged with its reason and changes nothing ---"
queue_task; ADVANCE_OUT='refused a review handback of task 2 from "bob": only the reviewer pane "rita" reviews, and the builder never reviews'
run_task_bridge
case "$OUT" in *"task 2: refused a review handback"*"the builder never reviews"*) ok "the bridge log says why it was refused" ;; *) nope "the bridge log lost the refusal: $OUT" ;; esac
[ -s "$BUILDER_TARGET_LOG" ] && nope "a refused handback typed into a pane" || ok "nothing is typed into any pane"
[ -s "$PROMPT_LOG" ] && nope "a refused handback woke the planner" || ok "a refused handback wakes nobody"
grep -q "^build stop" "$TLOG" && nope "a refused handback stopped the build" || ok "the build is not stopped"

echo "--- startup recovers a claimed task event and processes it once ---"
queue_task; ADVANCE_OUT="next 2"
mv "$MB/q/t1.json" "$MB/q/t1.json.taken"
run_task_bridge
grep -q -- "build advance --root $TMP --task 2" "$TLOG" && ok "the claimed event was recovered and processed" || nope "the claimed event was lost"
[ "$(grep -c '^build advance ' "$TLOG")" -eq 1 ] && ok "the recovered event is processed once" || nope "the recovered event was not processed exactly once"
grep -q -- "--event t1" "$TLOG" && grep -q -- "--created $EVENT_CREATED" "$TLOG" \
  && ok "recovery preserves the event metadata" || nope "recovery lost event metadata"
[ ! -e "$MB/q/t1.json" ] && [ -f "$MB/q/t1.json.done" ] && ok "the recovered event ends as handled" || nope "the recovered event was not marked handled"

echo "--- a handled task event is not replayed by the next start ---"
: > "$TLOG"; ADVANCE_OUT="next 2"
run_task_bridge
[ "$(grep -c '^build advance ' "$TLOG")" -eq 0 ] && ok "a handled event stays handled" || nope "a handled event was replayed"

echo "--- startup leaves completed planner messages claimed and never resends them ---"
queue_task; rm "$MB/q/t1.json"
for kind in question handback verdict; do
  jq -n --arg kind "$kind" '{id:$kind, kind:$kind, from:"builder", question:"already delivered", message:"already delivered"}' \
    > "$MB/q/$kind.json.taken"
done
run_task_bridge
[ ! -s "$PROMPT_LOG" ] && ok "completed planner messages are not resent" || nope "startup resent completed planner messages"
for kind in question handback verdict; do
  [ -f "$MB/q/$kind.json.taken" ] && [ ! -e "$MB/q/$kind.json" ] \
    && ok "the completed $kind stays claimed" || nope "the completed $kind was requeued"
done

echo "--- a task message with a string task and no sender is still advanced ---"
queue_task; : > "$PROMPT_LOG"; ADVANCE_OUT="next 2"
jq -n --arg created "$EVENT_CREATED" '{id:"t1", kind:"task", from:null, task:"2", review:null, report_path:"/r/x.md", created:$created}' > "$MB/q/t1.json"
run_task_bridge
grep -q -- "build advance --root $TMP --task 2 --event t1 --created $EVENT_CREATED --report /r/x.md\$" "$TLOG" \
  && ok "advance hears task 2, with no stuck or review flags" || nope "advance call ($(grep '^build advance' "$TLOG"))"

echo "--- a stuck task is passed on as stuck ---"
queue_task true; ADVANCE_OUT="stopped stuck R2 $MSGFILE"
run_task_bridge
grep -q -- "--stuck" "$TLOG" && ok "advance hears --stuck" || nope "advance hears --stuck"

echo "--- a finished or stopped build tells the planner ---"
for out in "finished R1 $MSGFILE" "stopped red R1 $MSGFILE"; do
  queue_task; : > "$PROMPT_LOG"; ADVANCE_OUT="$out"
  run_task_bridge
  [ "$(cat "$PROMPT_LOG")" = "the build message" ] && ok "${out%% *}: the planner gets the build's message" \
    || nope "${out%% *}: the planner gets the build's message (got: $(cat "$PROMPT_LOG"))"
  grep -q "^handoff" "$TLOG" && nope "${out%% *}: no next task is sent" || ok "${out%% *}: no next task is sent"
done

echo "--- a next task that cannot be sent stops the build and says why ---"
queue_task; : > "$PROMPT_LOG"; ADVANCE_OUT="next 3"; HANDOFF_RC=24
run_task_bridge
grep -q -- "build stop --root $TMP --reason handoff --detail tandem-handoff --continue exited 24: tandem-handoff: this plan has not cleared review." "$TLOG" \
  && ok "the stop records handoff's own reason" || nope "the stop records handoff's own reason ($(cat "$TLOG"))"
[ "$(cat "$PROMPT_LOG")" = "the build message" ] && ok "the planner is told" || nope "the planner is told"
HANDOFF_RC=0

echo "--- a stale task event changes nothing ---"
queue_task; : > "$PROMPT_LOG"; ADVANCE_OUT="ignored task 2 is not the current task (4)"
run_task_bridge
[ -s "$PROMPT_LOG" ] && nope "an ignored event woke the planner" || ok "an ignored event wakes nobody"
grep -q "^handoff" "$TLOG" && nope "an ignored event sent a task" || ok "an ignored event sends no task"

echo "--- advance failing pauses the build and tells the planner ---"
queue_task; : > "$PROMPT_LOG"; ADVANCE_OUT=""; ADVANCE_RC=1
run_task_bridge
case "$(cat "$PROMPT_LOG")" in *"could not check task 2"*) ok "the planner hears advance failed" ;; *) nope "the planner hears advance failed" ;; esac
ADVANCE_RC=0

echo "--- a handback for a build that is gone is dropped, not reported as a pause ---"
queue_task; : > "$PROMPT_LOG"; ADVANCE_OUT=""; ADVANCE_RC=1; ADVANCE_ERR="tandem build: no build in /x/.tandem-relay/build; tandem-handoff starts one"
run_task_bridge
[ -s "$PROMPT_LOG" ] && nope "a stale handback woke the planner" || ok "a stale handback wakes nobody"
[ -f "$MB/q/t1.json.done" ] && ok "and it is marked handled" || nope "the stale handback was left claimed"
ADVANCE_RC=0; ADVANCE_ERR=""

echo "--- a durable run: the loop waits on events, and a gone agent gets an observing tick ---"
# `tandem agent watch` is stubbed: WATCH_RC=27 means an agent is gone,
# anything else is a timeout. The bridge runs in the background and is killed
# after its turns.
cat > "$TMP/bin/tandem" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$TANDEM_LOG"
case "$1 $2" in
  "relay read") [ "${RELAY_READ_FAIL:-0}" = 1 ] && exit 1; exec "$TANDEM_REAL" "$@" ;;
  "state active-run"|"build roles") exec "$TANDEM_REAL" "$@" ;;
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

echo "--- --mailbox is required ---"
OUT=$(PATH="$TMP/bin:$PATH" HERDR_ENV=1 "$BRIDGE" --planner planner --once 2>&1); RC=$?
[ "$RC" -ne 0 ] && ok "refuses without --mailbox" || nope "should refuse without --mailbox"
chk "it names --mailbox as required" "--mailbox is required"

echo "--- refuses outside a Herdr pane ---"
OUT=$(env -u HERDR_ENV PATH="$TMP/bin:$PATH" "$BRIDGE" --mailbox "$MB" --once 2>&1); RC=$?
[ "$RC" -ne 0 ] && ok "refuses without HERDR_ENV" || nope "should refuse without HERDR_ENV"
chk "it names HERDR_ENV as the reason" "HERDR_ENV"

printf '\ntandem-bridge: passed %d, failed %d\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

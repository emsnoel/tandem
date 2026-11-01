#!/usr/bin/env bash
# tandem-handback hands finished work from the builder back to the planner.
#
# Hermetic: tandem, herdr and jq (symlinked) are stubbed. `tandem agent self
# --expect` is the real command, run against the stubbed herdr.
set -uo pipefail
HANDBACK_BIN="${1:-$(cd "$(dirname "$0")/.." && pwd)/bin/tandem-handback}"
[ -x "$HANDBACK_BIN" ] || { echo "not executable: $HANDBACK_BIN" >&2; exit 2; }

PASS=0; FAIL=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }
chk()  { case "$OUT" in *"$2"*) ok "$1" ;; *) nope "$1 (missing: $2)" ;; esac; }

command -v jq >/dev/null || { echo "need jq" >&2; exit 2; }

TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
mkdir -p "$TMP/bin" "$TMP/mailbox"
# A jq that fails: tandem-handback asks tandem, so any jq call is a bug. The
# test reads the queued messages with the real jq.
printf '#!/bin/sh\necho "jq was called: $*" >&2\nexit 99\n' > "$TMP/bin/jq"
(cd "$(dirname "$0")/.." && go build -o "$TMP/tandem-real" ./cmd/tandem) || { echo "could not build tandem" >&2; exit 2; }

PLAN="$TMP/p.md"; printf '# plan\n' > "$PLAN"
REPORT="$TMP/report.md"; printf 'all tasks done, suite green\n' > "$REPORT"

# The stub logs every tandem call and answers the reads tandem-handback makes.
# AGENT_SNAPSHOT_RC (0 found, 3 unreachable, else no agent), AGENT_STATUS_SEQ,
# AGENT_PROMPT_RC and AGENT_WAIT_RC choose the branch. herdr's `agent list`
# returns nothing, so SELF defaults to "builder" without HERDR_ROLE.
cat > "$TMP/bin/tandem" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$MARKER"
case "$1 $2" in
  # The real binary writes the messages and reads the run marker and the build.
  # ROLES_SLOW pauses before the last field, so a reader that stops early
  # makes this writer die of SIGPIPE.
  "build roles")
    [ -z "${ROLES_SLOW:-}" ] && exec "$TANDEM_REAL" "$@"
    printf '%s\0' /p.md builder reviewer; sleep 1; printf '%s\0' planner; exit 0 ;;
  "relay write"|"state active-run") exec "$TANDEM_REAL" "$@" ;;
  "agent snapshot")
    rc="${AGENT_SNAPSHOT_RC:-0}"
    [[ "$rc" == 0 ]] || exit "$rc"
    printf 'w7:p1\t%s\t%s\n' "$PWD" "${SNAPSHOT_STATUS:-idle}"
    exit 0 ;;
  "agent status")
    printf '%s\n' "${AGENT_STATUS_SEQ:-working}"
    exit 0 ;;
  "agent prompt")
    if [[ "${AGENT_PROMPT_RC:-0}" != 0 ]]; then
      printf 'send-failed\tsimulated prompt failure\n' >&2
      exit "${AGENT_PROMPT_RC}"
    fi
    # --as: CONFIRM_RC picks the outcome tandem agent prompt reports.
    case "${CONFIRM_RC:-0}" in
      10) printf 'agent_blocked\tstill blocked\n' >&2; exit 10 ;;
      11) printf 'agent_input_elsewhere\tplanner'"'"'s pane shows the screen of subagent "Fix it", whose input goes to that subagent; nothing was typed\n' >&2; exit 10 ;;
      25) printf 'agent_never_started\tno turn began\n' >&2; exit 25 ;;
      26) printf 'agent_turn_failed\tturn ended at once\n%s\n' "${PANE_TEXT:-}" >&2; exit 26 ;;
    esac
    exit 0 ;;
  "agent wait")
    exit "${AGENT_WAIT_RC:-0}" ;;
  "agent self")
    # --expect is the real decision, against the stubbed herdr below.
    [ "${3:-}" = --expect ] && exec "$TANDEM_REAL" "$@"
    # The pane Herdr says this is, or empty. AGENT_SELF_RC makes the lookup fail.
    [ "${AGENT_SELF_RC:-0}" = 0 ] || { printf 'herdr unreachable\n' >&2; exit "$AGENT_SELF_RC"; }
    [ -n "${AGENT_SELF:-}" ] && printf '%s\n' "$AGENT_SELF"
    exit 0 ;;
esac
case "$1" in
  paths) printf '%s\n' "$MAILBOX"; exit 0 ;;
esac
exit 0
STUB
# HERDR_FAKE makes herdr hang (timeout), fail as the Codex sandbox does
# (denied), or return an error (error). Otherwise w1:p1 is the builder and
# w1:p2 the reviewer.
cat > "$TMP/bin/herdr" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$MARKER"
case "${HERDR_FAKE:-}" in
  timeout) sleep 30; exit 0 ;;
  denied)  echo 'Error: Os { code: 1, kind: PermissionDenied, message: "Operation not permitted" }' >&2; exit 1 ;;
  error)   echo '{"error":{"code":"internal","message":"herdr failed"}}' >&2; exit 1 ;;
esac
[[ "$1 $2" == "agent list" ]] && { printf '{"result":{"agents":[{"pane_id":"w1:p1","name":"builder","agent_status":"idle"},{"pane_id":"w1:p2","name":"reviewer","agent_status":"idle"}]}}\n'; exit 0; }
exit 0
STUB
chmod +x "$TMP/bin"/*

MARKER="$TMP/calls"
# ENV adds per-case variables on top of the fixed base.
run() {
  : > "$MARKER"
  OUT=$(env -i PATH="$TMP/bin:/usr/bin:/bin" HOME="$HOME" TANDEM_REAL="$TMP/tandem-real" \
          MAILBOX="$TMP/mailbox" MARKER="$MARKER" HERDR_ENV=1 \
          ${ENV[@]+"${ENV[@]}"} "$@" 2>&1)
  RC=$?
}

# --- --help ----------------------------------------------------------------
ENV=()
run "$HANDBACK_BIN" --help
[[ "$RC" == 0 ]] && ok "--help exits 0" || nope "--help exits 0 (got $RC)"
chk "--help prints usage" "usage: tandem-handback"

# --- must run inside a Herdr pane -------------------------------------------
: > "$MARKER"
OUT=$(env -i PATH="$TMP/bin:/usr/bin:/bin" HOME="$HOME" \
        MAILBOX="$TMP/mailbox" MARKER="$MARKER" \
        "$HANDBACK_BIN" --report "$REPORT" 2>&1); RC=$?
[[ "$RC" == 1 ]] && ok "outside Herdr exits 1" || nope "outside Herdr exits 1 (got $RC)"
chk "outside Herdr names the reason" "not running inside a Herdr-managed pane"

# An engine setting cannot exempt legacy task delivery from the pane guard.
mkdir -p "$TMP/mailbox/build"
printf '{"state":"running"}\n' > "$TMP/mailbox/build/build.json"
ENV=(HERDR_ENV= TANDEM_BUILD_ENGINE=durable)
run "$HANDBACK_BIN" --task 2 --report "$REPORT"
[[ "$RC" != 0 ]] && ok "legacy task without pane environment is refused" || nope "legacy task bypassed pane guard"
chk "legacy task names the pane guard" "HERDR_ENV != 1"
[ -z "$(find "$TMP/mailbox" -type f ! -name build.json)" ] && ok "legacy refusal writes no queue or report" || nope "legacy refusal wrote files"

# A whole-plan call never gains the durable task exception.
printf '{"protocol_version":2,"run_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}\n' > "$TMP/mailbox/active-run.json"
ENV=(HERDR_ENV=)
run "$HANDBACK_BIN" --report "$REPORT"
[[ "$RC" != 0 ]] && ok "whole-plan handback with durable marker is refused outside Herdr" || nope "whole-plan handback bypassed pane guard"
chk "whole-plan call still names the pane guard" "HERDR_ENV != 1"
[ -z "$(find "$TMP/mailbox" -type f ! -name build.json ! -name active-run.json)" ] && ok "whole-plan refusal writes no queue or report" || nope "whole-plan refusal wrote files"
rm -rf "$TMP/mailbox"; mkdir -p "$TMP/mailbox"

# --- a missing prompt template is refused, not sent empty ------------------
NOTPL="$TMP/notpl"; mkdir -p "$NOTPL"
cp "$HANDBACK_BIN" "$NOTPL/tandem-handback"; cp -R "$(dirname "$HANDBACK_BIN")/lib" "$NOTPL/lib"
rm -f "$NOTPL/lib/handback-prompt.txt"
ENV=(HERDR_ROLE=builder)
run "$NOTPL/tandem-handback" --report "$REPORT" --dry-run
[[ "$RC" != 0 ]] && ok "a missing prompt template fails" || nope "a missing prompt template was not refused"
chk "and says which file is missing" "prompt template"

# --- missing prerequisites ---------------------------------------------------
TRIMMED="$TMP/trimmed-bin"; mkdir -p "$TRIMMED"
ln -s "$TMP/bin/jq" "$TRIMMED/jq"
ln -s "$(command -v bash)" "$TRIMMED/bash"
# herdr is deliberately absent from this PATH.
: > "$MARKER"
OUT=$(env -i PATH="$TRIMMED" HOME="$HOME" MAILBOX="$TMP/mailbox" MARKER="$MARKER" HERDR_ENV=1 \
        "$HANDBACK_BIN" --report "$REPORT" 2>&1); RC=$?
[[ "$RC" == 1 ]] && ok "missing herdr on PATH exits 1" || nope "missing herdr on PATH exits 1 (got $RC)"
chk "missing herdr on PATH names it" "herdr not found on PATH"

# --- invalid blocked-wait seconds -------------------------------------------
ENV=(HERDR_HANDBACK_BLOCKED_WAIT_SECONDS=notanumber HERDR_ROLE=builder)
run "$HANDBACK_BIN" --report "$REPORT"
[[ "$RC" == 1 ]] && ok "invalid blocked-wait seconds exits 1" || nope "invalid blocked-wait seconds exits 1 (got $RC)"
chk "invalid blocked-wait seconds names it" "HERDR_HANDBACK_BLOCKED_WAIT_SECONDS must be a positive integer"

# --- report validation --------------------------------------------------------
ENV=(HERDR_ROLE=builder)
run "$HANDBACK_BIN" --report "$TMP/no-such-report.md"
[[ "$RC" == 1 ]] && ok "missing report file exits 1" || nope "missing report file exits 1 (got $RC)"
chk "missing report file names it" "report file not found"

EMPTY="$TMP/empty.md"; printf '   \n\n' > "$EMPTY"
run "$HANDBACK_BIN" --report "$EMPTY"
[[ "$RC" == 1 ]] && ok "whitespace-only report exits 1" || nope "whitespace-only report exits 1 (got $RC)"
chk "whitespace-only report names it" "the report is empty"

# --- refuses to hand back to itself ------------------------------------------
ENV=(HERDR_ROLE=planner)
run "$HANDBACK_BIN" --report "$REPORT"
[[ "$RC" == 1 ]] && ok "handing back to self exits 1" || nope "handing back to self exits 1 (got $RC)"
chk "handing back to self names it" "refusing to hand work back to yourself"

# --- dry run: describes the action, writes nothing, sends nothing ------------
rm -rf "$TMP/mailbox"; mkdir -p "$TMP/mailbox"
ENV=(HERDR_ROLE=builder)
run "$HANDBACK_BIN" --report "$REPORT" --plan "$PLAN" --dry-run
[[ "$RC" == 0 ]] && ok "dry run exits 0" || nope "dry run exits 0 (got $RC)"
chk "dry run names the report it would write" "[dry-run] write report"
chk "dry run shows the message it would send" "--- message that would be sent ---"
chk "dry run's message names the plan" "Plan:   $PLAN"
if compgen -G "$TMP/mailbox/reports/*" > /dev/null 2>&1; then
  nope "dry run did not actually write a report"
else
  ok "dry run did not actually write a report"
fi
if grep -q 'agent prompt' "$MARKER"; then
  nope "dry run did not actually send anything"
else
  ok "dry run did not actually send anything"
fi

# --- sandboxed builder (socket unreachable): queues to the mailbox ----------
rm -rf "$TMP/mailbox"; mkdir -p "$TMP/mailbox"
ENV=(HERDR_ROLE=builder AGENT_SNAPSHOT_RC=3)
run "$HANDBACK_BIN" --report "$REPORT" --plan "$PLAN"
[[ "$RC" == 0 ]] && ok "sandboxed handback exits 0" || nope "sandboxed handback exits 0 (got $RC)"
chk "sandboxed handback says queued" "handback was queued at"
QFILE=$(ls "$TMP/mailbox/q"/*.json 2>/dev/null | head -1)
[[ -n "$QFILE" ]] && ok "a queued handback file was written" || nope "a queued handback file was written"
if [[ -n "$QFILE" ]]; then
  Q=$(cat "$QFILE")
  [[ "$(jq -r '.kind' <<<"$Q")" == "handback" ]] && ok "the queued file records kind=handback" || nope "the queued file records kind=handback"
  [[ "$(jq -r '.from' <<<"$Q")" == "builder" ]] && ok "the queued file records the sender" || nope "the queued file records the sender"
  [[ "$(jq -r '.to' <<<"$Q")" == "planner" ]] && ok "the queued file records the recipient" || nope "the queued file records the recipient"
  [[ "$(jq -r '.plan' <<<"$Q")" == "$PLAN" ]] && ok "the queued file records the plan path" || nope "the queued file records the plan path"
fi
REPFILE=$(ls "$TMP/mailbox/reports"/*.md 2>/dev/null | head -1)
[[ -n "$REPFILE" ]] && ok "the report was still written to disk" || nope "the report was still written to disk"

# --- no planner agent found (not sandboxed, just gone) -----------------------
rm -rf "$TMP/mailbox"; mkdir -p "$TMP/mailbox"
ENV=(HERDR_ROLE=builder AGENT_SNAPSHOT_RC=10)
run "$HANDBACK_BIN" --report "$REPORT"
[[ "$RC" == 2 ]] && ok "no planner agent exits 2" || nope "no planner agent exits 2 (got $RC)"
chk "no planner agent names it" "no agent named 'planner'"
chk "no planner agent points at the report" "Your report is safe at"
REPFILE=$(ls "$TMP/mailbox/reports"/*.md 2>/dev/null | head -1)
[[ -n "$REPFILE" ]] && ok "the report is still written when the planner is gone" || nope "the report is still written when the planner is gone"

# --- planner blocked: tandem waits it out inside --as -------------------
rm -rf "$TMP/mailbox"; mkdir -p "$TMP/mailbox"
ENV=(HERDR_ROLE=builder SNAPSHOT_STATUS=blocked HERDR_HANDBACK_BLOCKED_WAIT_SECONDS=7)
run "$HANDBACK_BIN" --report "$REPORT"
[[ "$RC" == 0 ]] && ok "blocked planner that clears: exits 0" || nope "blocked planner that clears: exits 0 (got $RC)"
chk "blocked planner that clears: hands back" "handed back to 'planner'"
if grep -q -e '--as handback --wait-unblocked 7000' "$MARKER"; then
  ok "blocked planner that clears: the wait budget goes to --wait-unblocked"
else
  nope "blocked planner that clears: the wait budget goes to --wait-unblocked"
fi

# --- planner blocked: still blocked after the budget -------------------------
rm -rf "$TMP/mailbox"; mkdir -p "$TMP/mailbox"
ENV=(HERDR_ROLE=builder SNAPSHOT_STATUS=blocked CONFIRM_RC=10 HERDR_HANDBACK_BLOCKED_WAIT_SECONDS=1)
run "$HANDBACK_BIN" --report "$REPORT"
[[ "$RC" == 3 ]] && ok "blocked planner that never clears: exits 3" || nope "blocked planner that never clears: exits 3 (got $RC)"
chk "blocked planner that never clears: names it" "was still blocked after"
chk "blocked planner that never clears: points at the report" "Your report is safe at"

# --- planner showing a subagent's screen ------------------------------
rm -rf "$TMP/mailbox"; mkdir -p "$TMP/mailbox"
ENV=(HERDR_ROLE=builder CONFIRM_RC=11 HERDR_HANDBACK_BLOCKED_WAIT_SECONDS=1)
run "$HANDBACK_BIN" --report "$REPORT"
[[ "$RC" == 3 ]] && ok "planner at a subagent's screen: exits 3" || nope "planner at a subagent's screen: exits 3 (got $RC)"
chk "planner at a subagent's screen: says what the pane shows" 'the screen of subagent "Fix it"'
chk "planner at a subagent's screen: says nothing was sent" "nothing was sent"
chk "planner at a subagent's screen: points at the report" "Your report is safe at"

# --- prompt delivery fails ----------------------------------------------------
rm -rf "$TMP/mailbox"; mkdir -p "$TMP/mailbox"
ENV=(HERDR_ROLE=builder AGENT_PROMPT_RC=1)
run "$HANDBACK_BIN" --report "$REPORT"
[[ "$RC" == 5 ]] && ok "prompt delivery failure exits 5" || nope "prompt delivery failure exits 5 (got $RC)"
chk "prompt delivery failure names the reason" "simulated prompt failure"
REPFILE=$(ls "$TMP/mailbox/reports"/*.md 2>/dev/null | head -1)
[[ -n "$REPFILE" ]] && ok "the report is still written when delivery fails" || nope "the report is still written when delivery fails"

# --- planner idle, never wakes: exit 7 after the poll loop (about 8s) ---
rm -rf "$TMP/mailbox"; mkdir -p "$TMP/mailbox"
ENV=(HERDR_ROLE=builder SNAPSHOT_STATUS=idle CONFIRM_RC=25)
run "$HANDBACK_BIN" --report "$REPORT"
[[ "$RC" == 7 ]] && ok "planner never wakes exits 7" || nope "planner never wakes exits 7 (got $RC)"
chk "planner never wakes warns" "never woke up"
chk "planner never wakes says do not resend" "Do NOT redo the work and do NOT send it again"

# --- planner whose turn ends at once: the handback says so and shows the pane
rm -rf "$TMP/mailbox"; mkdir -p "$TMP/mailbox"
ENV=(HERDR_ROLE=builder SNAPSHOT_STATUS=idle CONFIRM_RC=26 PANE_TEXT="model error text")
run "$HANDBACK_BIN" --report "$REPORT"
[[ "$RC" == 7 ]] && ok "ended-at-once handback exits 7" || nope "ended at once: rc=$RC"
chk "ended-at-once handback shows the pane" "model error text"

# --- planner idle, wakes on the first poll: success ---------------------------
rm -rf "$TMP/mailbox"; mkdir -p "$TMP/mailbox"
ENV=(HERDR_ROLE=builder SNAPSHOT_STATUS=idle AGENT_STATUS_SEQ=working)
run "$HANDBACK_BIN" --report "$REPORT"
[[ "$RC" == 0 ]] && ok "planner wakes on first poll: exits 0" || nope "planner wakes on first poll: exits 0 (got $RC)"
chk "planner wakes on first poll: hands back" "handed back to 'planner'"
chk "planner wakes on first poll: says stay idle" "Stay idle"

# --- planner already working before the prompt: no poll needed ---------------
rm -rf "$TMP/mailbox"; mkdir -p "$TMP/mailbox"
ENV=(HERDR_ROLE=builder SNAPSHOT_STATUS=working)
run "$HANDBACK_BIN" --report "$REPORT"
[[ "$RC" == 0 ]] && ok "planner already working: exits 0" || nope "planner already working: exits 0 (got $RC)"
chk "planner already working: hands back" "handed back to 'planner'"

# --- report can also arrive on stdin -----------------------------------------
rm -rf "$TMP/mailbox"; mkdir -p "$TMP/mailbox"
: > "$MARKER"
OUT=$(env -i PATH="$TMP/bin:/usr/bin:/bin" HOME="$HOME" MAILBOX="$TMP/mailbox" MARKER="$MARKER" \
        HERDR_ENV=1 HERDR_ROLE=builder \
        "$HANDBACK_BIN" --report - <<<"piped report body" 2>&1); RC=$?
[[ "$RC" == 0 ]] && ok "report via stdin (--report -): exits 0" || nope "report via stdin (--report -): exits 0 (got $RC)"
REPFILE=$(ls "$TMP/mailbox/reports"/*.md 2>/dev/null | head -1)
if [[ -n "$REPFILE" && "$(cat "$REPFILE")" == "piped report body" ]]; then
  ok "the piped report body was written verbatim"
else
  nope "the piped report body was written verbatim"
fi

# --- happy-path report is written atomically, with the exact body -----------
rm -rf "$TMP/mailbox"; mkdir -p "$TMP/mailbox"
ENV=(HERDR_ROLE=builder)
run "$HANDBACK_BIN" --report "$REPORT"
REPFILE=$(ls "$TMP/mailbox/reports"/*.md 2>/dev/null | head -1)
if [[ -n "$REPFILE" && "$(cat "$REPFILE")" == "all tasks done, suite green" ]]; then
  ok "the report body matches the input file"
else
  nope "the report body matches the input file"
fi
if compgen -G "$TMP/mailbox/reports/*.tmp" > /dev/null 2>&1; then
  nope "no .tmp report files were left behind (atomic rename)"
else
  ok "no .tmp report files were left behind (atomic rename)"
fi

# --- one task of a build ---
# A task handback always goes to the relay bridge, which drives the build.
TB="$TMP/mailbox/build"
ENV=(HERDR_ROLE=builder)
rm -rf "$TB" "$TMP/mailbox/q"
run "$HANDBACK_BIN" --task 2 --report "$REPORT"
[[ "$RC" == 1 ]] && ok "--task with no build running exits 1" || nope "--task with no build running exits 1 (got $RC)"
chk "--task with no build says so" "no build is running"

mkdir -p "$TB"; printf '{"state":"running"}\n' > "$TB/build.json"
run "$HANDBACK_BIN" --task 2 --report "$REPORT" --plan "$PLAN"
[[ "$RC" == 0 ]] && ok "--task exits 0" || nope "--task exits 0 (got $RC): $OUT"
Q=$(ls "$TMP/mailbox/q"/*.json 2>/dev/null | head -1)
# The bridge tests task with ^[0-9]+$ and stuck with == true, so the types matter.
if [[ -n "$Q" ]] && jq -e '.kind=="task" and .task==2 and (.task|type)=="number" and .stuck==false and (.review|type)=="boolean" and (.report_path|endswith("build/task-2.report.md"))' "$Q" >/dev/null; then
  ok "--task queues a task event for the bridge"
else
  nope "--task queues a task event for the bridge ($(cat "$Q" 2>/dev/null))"
fi
[[ "$(cat "$TB/task-2.report.md" 2>/dev/null)" == "all tasks done, suite green" ]] \
  && ok "the task report lands beside the build" || nope "the task report lands beside the build"
grep -q "agent prompt" "$MARKER" && nope "a task handback prompted the planner directly" \
  || ok "a task handback never prompts the planner"
chk "--task tells the builder to stop" "Stop here"

rm -rf "$TMP/mailbox/q"
run "$HANDBACK_BIN" --task 2 --stuck --report "$REPORT"
Q=$(ls "$TMP/mailbox/q"/*.json 2>/dev/null | head -1)
jq -e '.stuck==true' "$Q" >/dev/null 2>&1 && ok "--stuck is carried to the bridge" || nope "--stuck is carried to the bridge"

# A review comes from the reviewer pane and must not overwrite the builder's
# handback.
printf '{"state":"running","builder":"builder","reviewer":"reviewer"}\n' > "$TB/build.json"
REVIEW_REPORT="$TMP/review.md"; printf 'review found a bug\n' > "$REVIEW_REPORT"
rm -rf "$TMP/mailbox/q"
ENV=(HERDR_ROLE=reviewer)
run "$HANDBACK_BIN" --task 2 --review --verdict changes --report "$REVIEW_REPORT"
[[ "$RC" == 0 ]] && ok "--review exits 0" || nope "--review exits 0 (got $RC): $OUT"
Q=$(ls "$TMP/mailbox/q"/*.json 2>/dev/null | head -1)
if [[ -n "$Q" ]] && jq -e '.kind=="task" and .task==2 and .review==true and .verdict=="changes" and .from=="reviewer" and (.report_path|endswith("build/task-2.review.md"))' "$Q" >/dev/null; then
  ok "--review queues the reviewer's verdict and its separate report path"
else
  nope "--review queues the reviewer's verdict and its separate report path ($(cat "$Q" 2>/dev/null))"
fi
[[ "$(cat "$TB/task-2.report.md" 2>/dev/null)" == "all tasks done, suite green" ]] \
  && ok "--review preserves the first report" || nope "--review preserves the first report"
[[ "$(cat "$TB/task-2.review.md" 2>/dev/null)" == "review found a bug" ]] \
  && ok "--review writes the second report" || nope "--review writes the second report"
# Reading only the fields it needs must not cut build roles off mid-answer.
rm -rf "$TMP/mailbox/q"
ENV=(HERDR_ROLE=reviewer ROLES_SLOW=1)
run "$HANDBACK_BIN" --task 2 --review --verdict clean --report "$REVIEW_REPORT"
[[ "$RC" == 0 ]] && ok "a slow build roles answer still names the reviewer" || nope "a slow build roles answer lost the reviewer (rc $RC): $OUT"
# --role names the pane when the CLI does not pass HERDR_ROLE to its commands.
rm -rf "$TMP/mailbox/q"
ENV=()
run "$HANDBACK_BIN" --task 2 --review --role reviewer --verdict clean --report "$REVIEW_REPORT"
Q=$(ls "$TMP/mailbox/q"/*.json 2>/dev/null | head -1)
[[ "$RC" == 0 ]] && jq -e '.from=="reviewer" and .verdict=="clean"' "$Q" >/dev/null 2>&1 \
  && ok "--role reviewer identifies the reviewer pane" || nope "--role reviewer identifies the reviewer pane (rc $RC): $OUT"
# Where Herdr can name the pane, that decides, not a claimed role.
for env_case in "HERDR_PANE_ID=w1:p1" "HERDR_PANE_ID=w1:p1 HERDR_ROLE=reviewer"; do
  rm -rf "$TMP/mailbox/q"
  read -r -a ENV <<<"$env_case"
  run "$HANDBACK_BIN" --task 2 --review --role reviewer --verdict clean --report "$REVIEW_REPORT"
  [[ "$RC" == 1 && ! -d "$TMP/mailbox/q" ]] && ok "a builder claiming the reviewer role is refused ($env_case)" \
    || nope "a builder claiming the reviewer role is refused ($env_case): rc $RC, $OUT"
done
chk "the refusal says the review runs in the reviewer pane" "Run this from the reviewer pane"
chk "and that the builder never reviews" "The builder never reviews. NOTHING WAS SENT."
case "$OUT" in *"--role"*) nope "the refusal coaches a --role: $OUT" ;; *) ok "the refusal coaches no --role" ;; esac
rm -rf "$TMP/mailbox/q"
ENV=(HERDR_PANE_ID=w1:p2)
run "$HANDBACK_BIN" --task 2 --review --verdict clean --report "$REVIEW_REPORT"
Q=$(ls "$TMP/mailbox/q"/*.json 2>/dev/null | head -1)
[[ "$RC" == 0 ]] && jq -e '.from=="reviewer"' "$Q" >/dev/null 2>&1 \
  && ok "the pane Herdr names as the reviewer needs no --role" || nope "the pane Herdr names as the reviewer needs no --role (rc $RC): $OUT"
# In a Herdr pane, a failed, timed-out or empty lookup fails closed.
for env_case in "HERDR_PANE_ID=w1:p1 HERDR_FAKE=error" "HERDR_PANE_ID=w1:p9" "HERDR_PANE_ID=w1:p1 HERDR_FAKE=timeout"; do
  rm -rf "$TMP/mailbox/q"
  read -r -a ENV <<<"$env_case"
  run "$HANDBACK_BIN" --task 2 --review --role reviewer --verdict clean --report "$REVIEW_REPORT"
  [[ "$RC" == 1 && ! -d "$TMP/mailbox/q" ]] && ok "a pane Herdr cannot name is refused, nothing queued ($env_case)" \
    || nope "a pane Herdr cannot name is refused ($env_case): rc $RC, $OUT"
  chk "the refusal says to retry ($env_case)" "try again"
  chk "and to check Herdr is running ($env_case)" "Herdr is running"
done
# A sandboxed reviewer has no HERDR_PANE_ID, so its role names it.
rm -rf "$TMP/mailbox/q"
ENV=()
run "$HANDBACK_BIN" --task 2 --review --role reviewer --verdict clean --report "$REVIEW_REPORT"
[[ "$RC" == 0 ]] && ok "with no pane id, --role reviewer is still accepted" || nope "with no pane id, --role reviewer is still accepted (rc $RC): $OUT"
rm -rf "$TMP/mailbox/q"
run "$HANDBACK_BIN" --task 2 --review --role builder --verdict clean --report "$REVIEW_REPORT"
[[ "$RC" == 1 && ! -d "$TMP/mailbox/q" ]] && ok "with no pane id, --role builder is refused" || nope "with no pane id, --role builder is refused (rc $RC): $OUT"
# A sandboxed Codex reviewer can see a pane id from its shared daemon while
# its sandbox denies the socket. Its role names it, with a warning.
rm -rf "$TMP/mailbox/q"
ENV=(HERDR_PANE_ID=w1:p1 HERDR_FAKE=denied)
run "$HANDBACK_BIN" --task 2 --review --role reviewer --verdict clean --report "$REVIEW_REPORT"
Q=$(ls "$TMP/mailbox/q"/*.json 2>/dev/null | head -1)
[[ "$RC" == 0 ]] && jq -e '.from=="reviewer"' "$Q" >/dev/null 2>&1 \
  && ok "an unreachable socket falls back to --role reviewer" || nope "an unreachable socket falls back to --role reviewer (rc $RC): $OUT"
chk "and warns that Herdr cannot be reached" "warning: Herdr cannot be reached from this shell"
rm -rf "$TMP/mailbox/q"
run "$HANDBACK_BIN" --task 2 --review --role builder --verdict clean --report "$REVIEW_REPORT"
[[ "$RC" == 1 && ! -d "$TMP/mailbox/q" ]] && ok "an unreachable socket with --role builder is refused" \
  || nope "an unreachable socket with --role builder is refused (rc $RC): $OUT"
ENV=(HERDR_ROLE=reviewer)
for bad in "" maybe; do
  rm -rf "$TMP/mailbox/q"
  run "$HANDBACK_BIN" --task 2 --review ${bad:+--verdict "$bad"} --report "$REVIEW_REPORT"
  [[ "$RC" == 1 && ! -d "$TMP/mailbox/q" ]] && ok "--review with verdict '${bad}' is refused, nothing queued" \
    || nope "--review with verdict '${bad}' is refused (rc $RC)"
done
chk "it names the two verdicts" "--verdict clean or --verdict changes"
rm -rf "$TMP/mailbox/q"
run "$HANDBACK_BIN" --task 2 --review --verdict clean --stuck --report "$REVIEW_REPORT"
[[ "$RC" == 1 && ! -d "$TMP/mailbox/q" ]] && ok "--review with --stuck is refused" || nope "--review with --stuck is refused (rc $RC)"
run "$HANDBACK_BIN" --task 2 --review --stuck --run r1 --attempt a --job j --effect e --nonce n
[[ "$RC" == 1 ]] && ok "--review with --stuck is refused for a durable handback too" || nope "--review with --stuck is refused for a durable handback (rc $RC)"
chk "and says why" "--review takes no --stuck"
run "$HANDBACK_BIN" --task 2 --verdict clean --report "$REPORT"
[[ "$RC" == 1 ]] && ok "--verdict without --review exits 1" || nope "--verdict without --review exits 1 (got $RC)"
ENV=(HERDR_ROLE=builder)
run "$HANDBACK_BIN" --review --report "$REPORT"
[[ "$RC" == 1 ]] && ok "--review without --task exits 1" || nope "--review without --task exits 1 (got $RC)"

run "$HANDBACK_BIN" --task two --report "$REPORT"
[[ "$RC" == 1 ]] && ok "a task that is not a number exits 1" || nope "a task that is not a number exits 1 (got $RC)"
run "$HANDBACK_BIN" --stuck --report "$REPORT"
[[ "$RC" == 1 ]] && ok "--stuck without --task exits 1" || nope "--stuck without --task exits 1 (got $RC)"
rm -rf "$TB" "$TMP/mailbox/q"

printf '\ntandem-handback: passed %d, failed %d\n' "$PASS" "$FAIL"
(( FAIL == 0 ))

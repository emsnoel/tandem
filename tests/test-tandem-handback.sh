#!/usr/bin/env bash
# tandem-handback hands finished work from the builder back to the planner.
#
# Hermetic: tandem, herdr and jq are stubbed, and jq fails. `tandem agent self
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
  # The real binary writes the messages and reads the run marker.
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

mkdir -p "$TMP/mailbox"
# A whole-plan call never gains the durable task exception.
printf '{"protocol_version":2,"run_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}\n' > "$TMP/mailbox/active-run.json"
ENV=(HERDR_ENV=)
run "$HANDBACK_BIN" --report "$REPORT"
[[ "$RC" != 0 ]] && ok "whole-plan handback with durable marker is refused outside Herdr" || nope "whole-plan handback bypassed pane guard"
chk "whole-plan call still names the pane guard" "HERDR_ENV != 1"
[ -z "$(find "$TMP/mailbox" -type f ! -name active-run.json)" ] && ok "whole-plan refusal writes no queue or report" || nope "whole-plan refusal wrote files"
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

# --- one task needs a run ---
# A task handback belongs to the active run. With none there is no task.
ENV=(HERDR_ROLE=builder)
rm -rf "$TMP/mailbox/q"
run "$HANDBACK_BIN" --task 2 --report "$REPORT"
[[ "$RC" == 1 ]] && ok "--task with no run exits 1" || nope "--task with no run exits 1 (got $RC)"
chk "--task with no run says no run is active" "no run is active"
[ ! -d "$TMP/mailbox/q" ] && ok "--task with no run queues nothing" || nope "--task with no run queued something"

for removed in --review "--verdict clean"; do
  # shellcheck disable=SC2086
  run "$HANDBACK_BIN" --task 2 $removed --report "$REPORT"
  [[ "$RC" == 1 ]] && chk "$removed is an unknown argument" "unknown argument" || nope "$removed exits 1 (got $RC)"
done

run "$HANDBACK_BIN" --task two --report "$REPORT"
[[ "$RC" == 1 ]] && ok "a task that is not a number exits 1" || nope "a task that is not a number exits 1 (got $RC)"
run "$HANDBACK_BIN" --stuck --report "$REPORT"
[[ "$RC" == 1 ]] && ok "--stuck without --task exits 1" || nope "--stuck without --task exits 1 (got $RC)"
rm -rf "$TMP/mailbox/q"

printf '\ntandem-handback: passed %d, failed %d\n' "$PASS" "$FAIL"
(( FAIL == 0 ))

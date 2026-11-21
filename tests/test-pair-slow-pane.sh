#!/usr/bin/env bash
# tandem-pair must wait patiently when a newly split pane's shell starts slowly,
# and close any pane it created if launching the agent still fails (#89).
# Also verifies non-dry-run record_builder output includes the builder model (#92).
#
# Hermetic: tandem is stubbed; no Herdr server or panes are touched.
set -uo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)

PASS=0; FAIL=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }

TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
mkdir -p "$TMP/bin" "$TMP/repo"

# Stub tandem to record pane close calls and simulate slow-shell retries.
cat > "$TMP/bin/tandem" <<'STUB'
#!/usr/bin/env bash
case "$1 $2" in
  "pane wait-shell")
    printf '%s\n' "$*" >> "$LOG/wait_shell"
    exit 0 ;;
  "pane split")
    printf '%s\n' "${SPLIT_PANE_ID:-w1:p9}"
    exit 0 ;;
  "pane close")
    printf '%s\n' "$3" >> "$LOG/closed"
    exit 0 ;;
  "pane split-direction")
    exit 1 ;;
  "agent start")
    n=0
    [ -f "$LOG/attempts" ] && n=$(cat "$LOG/attempts")
    n=$((n + 1))
    printf '%s\n' "$n" > "$LOG/attempts"
    if [ "${START_MODE:-}" = "not_ready" ]; then
      printf 'agent_not_ready\tagent did not become ready\n' >&2
      exit 1
    fi
    if [ "${START_MODE:-}" = "fatal" ]; then
      printf 'start_failed\tbinary not found\n' >&2
      exit 1
    fi
    if [ "$n" -le "${FAIL_SHELL_COUNT:-0}" ]; then
      printf 'invalid_target\tagent target pane %s is not an available shell\n' "${SPLIT_PANE_ID:-w1:p9}" >&2
      exit 1
    fi
    exit 0 ;;
  "agent alive")
    exit 0 ;;
  "agent prelaunch")
    exit 0 ;;
  "config record-builder")
    printf 'copilot\0auto\0claude-sonnet-5.5\0{"kind":"copilot","autonomy":"auto","model":"claude-sonnet-5.5","adopted":false}\0'
    exit 0 ;;
  "paths relay")
    printf '%s/.tandem-relay\n' "$TMP/repo"
    exit 0 ;;
esac
exit 0
STUB
chmod +x "$TMP/bin/tandem"

run_case() {
  local mode="$1" fail_count="$2" role="$3"
  LOG="$TMP/log-$RANDOM"
  mkdir -p "$LOG"
  OUT=$(
    export PATH="$TMP/bin:$PATH" LOG TMP
    export START_MODE="$mode" FAIL_SHELL_COUNT="$fail_count" SPLIT_PANE_ID="w1:p9"
    export HERDR_PAIR_RETRY_DELAY_S=0 HERDR_PAIR_SHELL_WAIT_MS=100
    bash -c '
      set -euo pipefail
      die()  { printf "tandem-pair: %s\n" "$*" >&2; exit 1; }
      info() { printf "tandem-pair: %s\n" "$*"; }
      DRY_RUN=0; SKIP_DOCTOR=1; DIRECTION="right"; START_TIMEOUT_MS=5000
      PLANNER_PANE="w1:p1"; BUILDER_PANE=""; BUILDER_ADOPT=0; ADOPTED_KIND=""
      BUILDER_NAME="builder"; BUILDER_KIND="copilot"; BUILDER_ARGS=(--model claude-sonnet-5.5)
      OV_BUILDER_KIND="copilot"; OV_BUILDER_MODEL="claude-sonnet-5.5"; OV_BUILDER_EFFORT=""; OV_BUILDER_AUTONOMY=""
      REVIEWER_PANE=""; REVIEWER_NAME="reviewer"; REVIEWER_KIND="claude"; REVIEWER_LAUNCH=1
      REVIEWER_ARGS=(); OV_REVIEWER_KIND="claude"
      ROOT="$TMP/repo"; SPLIT_CWD="$TMP/repo"; BCWD="$TMP/repo"
      FIELDS=$(mktemp); trap "rm -f \"$FIELDS\"" EXIT
      . "'"$ROOT"'/bin/lib/pair-launch.sh"
      . "'"$ROOT"'/bin/lib/pair-panes.sh"
      . "'"$ROOT"'/bin/lib/pair-records.sh"
      case "'"$role"'" in
        builder)  ensure_builder ;;
        reviewer) BUILDER_PANE="w1:p2"; ensure_reviewer ;;
        record)   record_builder ;;
      esac
    ' 2>&1
  )
  RC=$?
}

# 1. Slow shell that becomes ready on attempt 4 (past the old 2-attempt limit).
run_case shell 3 builder
[ "$RC" -eq 0 ] && ok "slow shell ready on attempt 4 succeeds" || nope "slow shell ready on attempt 4 failed (rc $RC: $OUT)"
[ "$(cat "$LOG/attempts" 2>/dev/null || echo 0)" = "4" ] && ok "retried 4 times until shell was ready" || nope "attempts=$(cat "$LOG/attempts" 2>/dev/null)"
[ ! -s "$LOG/closed" ] && ok "did not close the pane when start succeeded" || nope "unexpectedly closed pane: $(cat "$LOG/closed")"

# 2. Slow shell that never becomes ready exhausts retries and closes the created pane.
run_case shell 20 builder
[ "$RC" -ne 0 ] && ok "persistent slow shell fails cleanly" || nope "persistent slow shell unexpectedly succeeded"
[ "$(cat "$LOG/attempts" 2>/dev/null || echo 0)" = "8" ] && ok "tried all 8 attempts before giving up" || nope "attempts=$(cat "$LOG/attempts" 2>/dev/null)"
[ "$(cat "$LOG/closed" 2>/dev/null || true)" = "w1:p9" ] && ok "closed newly created builder pane w1:p9 after shell timeout" || nope "closed=$(cat "$LOG/closed" 2>/dev/null)"

# 3. Non-shell start failure does not retry and closes the created pane immediately.
run_case fatal 0 builder
[ "$RC" -ne 0 ] && ok "fatal start error fails cleanly" || nope "fatal start error unexpectedly succeeded"
[ "$(cat "$LOG/attempts" 2>/dev/null || echo 0)" = "1" ] && ok "fatal error does not retry" || nope "attempts=$(cat "$LOG/attempts" 2>/dev/null)"
[ "$(cat "$LOG/closed" 2>/dev/null || true)" = "w1:p9" ] && ok "closed newly created builder pane on fatal start error" || nope "closed=$(cat "$LOG/closed" 2>/dev/null)"

# 4. agent_not_ready keeps the pane open (the agent started, just at a dialog).
run_case not_ready 0 builder
[ "$RC" -eq 0 ] && ok "agent_not_ready warns without aborting" || nope "agent_not_ready failed (rc $RC: $OUT)"
[ ! -s "$LOG/closed" ] && ok "agent_not_ready leaves the pane open" || nope "agent_not_ready closed pane: $(cat "$LOG/closed")"

# 5. Reviewer pane start failure closes the newly created reviewer pane too.
run_case shell 20 reviewer
[ "$RC" -ne 0 ] && ok "reviewer start failure fails cleanly" || nope "reviewer start failure unexpectedly succeeded"
[ "$(cat "$LOG/closed" 2>/dev/null || true)" = "w1:p9" ] && ok "closed newly created reviewer pane w1:p9 after start failure" || nope "closed=$(cat "$LOG/closed" 2>/dev/null)"

# 6. record_builder displays the model alongside kind/autonomy (#92).
run_case shell 0 record
[ "$RC" -eq 0 ] && ok "record_builder succeeds" || nope "record_builder failed (rc $RC: $OUT)"
case "$OUT" in
  *"recorded builder identity: copilot/auto on claude-sonnet-5.5"*) ok "record_builder prints kind/autonomy and model" ;;
  *) nope "record_builder output missing model: $OUT" ;;
esac

printf '\npair-slow-pane: passed %d, failed %d\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

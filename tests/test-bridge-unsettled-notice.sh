#!/usr/bin/env bash
# Targeted test for Issue #111:
# When a queued handback repeatedly fails to apply with "has not settled" for
# at least TANDEM_UNSETTLED_NOTICE_SECONDS, tandem-bridge logs once and sends
# one planner notice naming the builder pane, the `herdr agent explain` rule
# holding it in working, and the Ctrl+L workaround. It must not spam on retries.
set -uo pipefail
ROOT_DIR=$(cd "$(dirname "$0")/.." && pwd)
BRIDGE="$ROOT_DIR/bin/tandem-bridge"

PASS=0; FAIL=0
ok()    { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope()  { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }
has()   { case "$2" in *"$3"*) ok "$1" ;; *) nope "$1 (missing: $3)" ;; esac; }
hasno() { case "$2" in *"$3"*) nope "$1 (unexpected: $3)" ;; *) ok "$1" ;; esac; }

TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
mkdir -p "$TMP/bin" "$TMP/repo/.tandem-relay/durable/run1/q"
printf '#!/bin/sh\necho "jq was called: $*" >&2\nexit 99\n' > "$TMP/bin/jq"
chmod +x "$TMP/bin/jq"

MB="$TMP/repo/.tandem-relay"
printf '{"run_id":"run1"}\n' > "$MB/active-run.json"
printf '{"kind":"builder_submitted"}\n' > "$MB/durable/run1/q/ev1.json.taken"

PROMPT_COUNT="$TMP/prompt-count"; : > "$PROMPT_COUNT"
PROMPT_LOG="$TMP/prompt.txt"

cat > "$TMP/bin/herdr" <<'STUB'
#!/usr/bin/env bash
case "$1 $2" in
  "agent explain")
    printf 'state: working\nmatched rule: codex-spinner (pattern "Working" on line 24)\n'
    exit 0 ;;
esac
exit 0
STUB

cat > "$TMP/bin/tandem" <<'STUB'
#!/usr/bin/env bash
case "$1 $2" in
  "state active-run") printf 'run1\n'; exit 0 ;;
  "paths names")      printf 'planner\tbuilder\treviewer\n'; exit 0 ;;
  "agent status")     printf '%s\n' "${BUILDER_ST:-working}"; exit 0 ;;
  "agent snapshot")   printf 'w1:builder\t%s\t%s\n' "$REPO" "${BUILDER_ST:-working}"; exit 0 ;;
  "build event")
    if [ "${INGEST_OK:-0}" = 1 ]; then
      printf 'applied\0ok\0'
      exit 0
    fi
    printf 'tandem build event: the builder session has not settled; retry the submission when it has\n' >&2
    exit 1 ;;
  "build tick")
    printf 'run1\0running\0\0\0'
    exit 0 ;;
  "agent prompt")
    printf '1\n' >> "$PROMPT_COUNT"
    printf '%s' "$4" > "$PROMPT_LOG"
    exit 0 ;;
esac
exit 0
STUB
chmod +x "$TMP/bin/herdr" "$TMP/bin/tandem"

run_pass() {
  OUT=$(PATH="$TMP/bin:/usr/bin:/bin" HERDR_ENV=1 REPO="$TMP/repo" \
    PROMPT_COUNT="$PROMPT_COUNT" PROMPT_LOG="$PROMPT_LOG" \
    TANDEM_UNSETTLED_NOTICE_SECONDS="${1:-180}" \
    BUILDER_ST="${BUILDER_ST:-working}" INGEST_OK="${INGEST_OK:-0}" \
    "$BRIDGE" --mailbox "$MB" --planner planner --once 2>&1); RC=$?
}

printf '== before threshold: no notice is sent ==\n'
run_pass 180
[ "$RC" -eq 0 ] && ok "first pass exits 0" || nope "first pass rc $RC: $OUT"
[ -f "$MB/durable/run1/q/ev1.json.taken.unsettled-since" ] && ok "records first-seen unsettled timestamp" || nope "missing unsettled-since file"
[ ! -f "$MB/durable/run1/q/ev1.json.taken.unsettled-notified" ] && ok "not yet notified before threshold" || nope "notified too early"
[ "$(wc -l < "$PROMPT_COUNT" | tr -d ' ')" = "0" ] && ok "no planner prompt before threshold" || nope "sent prompt before threshold"

printf '\n== once threshold elapses: logs once and sends one planner notice ==\n'
# Backdate unsettled-since by 200 seconds so threshold (180s) has elapsed.
printf '%s\n' "$(( $(date +%s) - 200 ))" > "$MB/durable/run1/q/ev1.json.taken.unsettled-since"
run_pass 180
[ "$RC" -eq 0 ] && ok "pass past threshold exits 0" || nope "past-threshold pass rc $RC: $OUT"
has "bridge logs the stall notice once" "$OUT" "notifying planner"
[ "$(wc -l < "$PROMPT_COUNT" | tr -d ' ')" = "1" ] && ok "sent exactly one planner notice" || nope "prompt count: $(wc -l < "$PROMPT_COUNT")"
MSG=$(cat "$PROMPT_LOG")
has "notice names the builder pane" "$MSG" "w1:builder"
has "notice includes herdr agent explain rule" "$MSG" "codex-spinner"
has "notice includes Ctrl+L workaround" "$MSG" "Ctrl+L"

printf '\n== subsequent retries do not spam ==\n'
run_pass 180
[ "$RC" -eq 0 ] && ok "retry pass exits 0" || nope "retry pass rc $RC: $OUT"
hasno "retry does not log notifying planner again" "$OUT" "notifying planner"
[ "$(wc -l < "$PROMPT_COUNT" | tr -d ' ')" = "1" ] && ok "still only one planner notice after retry" || nope "spammed: $(wc -l < "$PROMPT_COUNT")"

printf '\n== once settled and applied: state markers are cleaned up ==\n'
BUILDER_ST=idle INGEST_OK=1 run_pass 180
[ "$RC" -eq 0 ] && ok "settled pass exits 0" || nope "settled pass rc $RC: $OUT"
[ ! -e "$MB/durable/run1/q/ev1.json.taken.unsettled-since" ] && ok "unsettled-since removed after apply" || nope "unsettled-since left behind"
[ ! -e "$MB/durable/run1/q/ev1.json.taken.unsettled-notified" ] && ok "unsettled-notified removed after apply" || nope "unsettled-notified left behind"

printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

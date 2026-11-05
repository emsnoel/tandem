#!/usr/bin/env bash
# A pause note the planner did not receive is sent again. The bridge runs one
# pass per call (--once) against a stub tandem whose `agent prompt` fails while
# FAILS_LEFT holds a number above zero. The retry time is moved by rewriting
# the pause-retry-at file, so nothing waits for 30 seconds.
set -uo pipefail
ROOT_DIR=$(cd "$(dirname "$0")/.." && pwd)
BRIDGE="$ROOT_DIR/bin/tandem-bridge"

PASS=0; FAIL=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }
has()  { case "$2" in *"$3"*) ok "$1" ;; *) nope "$1 (missing: $3): $2" ;; esac; }

command -v jq >/dev/null || { echo "need jq" >&2; exit 2; }
TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
(cd "$ROOT_DIR" && go build -o "$TMP/tandem-real" ./cmd/tandem) || { echo "could not build tandem"; exit 1; }
mkdir -p "$TMP/bin"

RUN=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
cat > "$TMP/bin/herdr" <<'STUB'
#!/usr/bin/env bash
exit 0
STUB
# tandem: relay paths from --root; tick answers TICK_OUT; a prompt fails while
# the FAILS_LEFT file holds a number above zero, and is saved otherwise.
cat > "$TMP/bin/tandem" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$CALLS"
case "$1" in
  paths) shift; while (( $# )); do [[ "$1" == --root ]] && { printf '%s/.tandem-relay\n' "$2"; exit 0; }; shift; done; exit 1 ;;
  build)
    case "$2" in
      tick|recover) printf '%s|' "$TICK_OUT" | tr '|' '\000'; exit 0 ;;
      status) printf '%s|' "$STATUS_OUT" | tr '|' '\000'; exit 0 ;;
    esac
    [[ "$2" == roles ]] && exec "$TANDEM_REAL" "$@" ;;
  state) [[ "$2" == active-run ]] && { echo "$RUN_ID"; exit 0; }; exec "$TANDEM_REAL" "$@" ;;
  relay) exec "$TANDEM_REAL" "$@" ;;
  agent)
    [[ "$2" == status ]] && { echo idle; exit 0; }
    if [[ "$2" == prompt ]]; then
      left=$(cat "$FAILS_LEFT" 2>/dev/null); left=${left:-0}
      if (( left > 0 )); then
        echo $(( left - 1 )) > "$FAILS_LEFT"
        printf 'planner_gone\tno pane\n' >&2
        exit 1
      fi
      printf '%s' "$4" > "$PROMPTS/$(ls "$PROMPTS" | wc -l | tr -d ' ')"
    fi ;;
esac
exit 0
STUB
chmod +x "$TMP/bin"/*

B="$TMP/repo"; REL="$B/.tandem-relay"; RD="$REL/durable/$RUN"
mkdir -p "$RD"/{q,payloads,receipts,quarantine}; git -C "$B" init -q .
printf '{"schema_version":2,"run_id":"%s","tasks":[{"ordinal":1,"id":"t1"}]}\n' "$RUN" > "$RD/manifest.json"
printf '{"engine_version":2,"protocol_version":2,"run_id":"%s","project_id":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}\n' "$RUN" > "$REL/active-run.json"
CALLS="$TMP/calls"; PROMPTS="$TMP/prompts"; FAILS_LEFT="$TMP/fails"
mkdir -p "$PROMPTS"
STATUS_OUT='paused|stuck||It said stuck.|task-1|builder|reviewer'
export CALLS PROMPTS FAILS_LEFT STATUS_OUT

# br: one bridge pass. TICK_OUT is the run's state.
br() {
  : > "$CALLS"
  OUT=$(env -i PATH="$TMP/bin:/usr/bin:/bin" HOME="$HOME" HERDR_ENV=1 CALLS="$CALLS" TANDEM_REAL="$TMP/tandem-real" \
          RUN_ID="$RUN" PROMPTS="$PROMPTS" FAILS_LEFT="$FAILS_LEFT" STATUS_OUT="$STATUS_OUT" TICK_OUT="$TICK_OUT" \
          "$BRIDGE" --mailbox "$REL" --planner planner --once 2>&1); RC=$?
}
sent() { ls "$PROMPTS" | wc -l | tr -d ' '; }
ticks() { grep -c '^build tick' "$CALLS"; }
# due: the retry time has passed.
due() { echo 0 > "$RD/pause-retry-at"; }
reset() { rm -rf "$PROMPTS" "$RD"/pause-*; mkdir -p "$PROMPTS"; echo "$1" > "$FAILS_LEFT"; }

printf '== a pause note the planner did not get is sent again ==\n'
TICK_OUT="$RUN|paused|stuck"
reset 2
br "$REL"
[ "$(sent)" = 0 ] && ok "the first send fails" || nope "sent $(sent)"
[ ! -f "$RD/pause-reported" ] && ok "a failed send leaves no mark" || nope "the mark was written before delivery"
[ "$(cat "$RD/pause-retry-delay" | cut -f1)" = 30 ] && ok "the first retry waits 30 seconds" || nope "delay: $(cat "$RD/pause-retry-delay")"
at=$(cat "$RD/pause-retry-at"); now=$(date +%s)
[ "$at" -gt "$((now+20))" ] && [ "$at" -le "$((now+30))" ] && ok "the retry time is about 30 seconds ahead" || nope "retry at $at, now $now"

br "$REL"
[ "$(sent)" = 0 ] && ! grep -q 'prompt' "$CALLS" && ok "not resent early" || nope "a pass before the retry time sent ($(cat "$CALLS"))"

due; br "$REL"
[ "$(sent)" = 0 ] && [ "$(cut -f1 "$RD/pause-retry-delay")" = 60 ] && ok "the second failure doubles the delay" || nope "delay: $(cat "$RD/pause-retry-delay")"

due; br "$REL"
[ "$(sent)" = 1 ] && ok "pause note is retried: a third pass after the retry time delivers it" || nope "sent $(sent): $OUT"
has "the note is the pause note" "$(cat "$PROMPTS/0" 2>/dev/null)" "[build] Run $RUN stopped: the builder said it could not finish the task."
[ "$(cat "$RD/pause-reported")" = "$RUN stuck" ] && ok "the mark is written after the send" || nope "mark: $(cat "$RD/pause-reported" 2>/dev/null)"
[ ! -e "$RD/pause-retry-at" ] && [ ! -e "$RD/pause-retry-delay" ] && ok "success clears the retry files" || nope "retry files remain"

br "$REL"; due; br "$REL"
[ "$(sent)" = 1 ] && ok "not sent twice" || nope "sent $(sent)"

printf '\n== the delay doubles to 300 seconds and stops ==\n'
reset 99
for _ in 1 2 3 4 5 6 7; do due; br "$REL"; done
[ "$(cut -f1 "$RD/pause-retry-delay")" = 300 ] && ok "the delay stops at 300" || nope "delay: $(cat "$RD/pause-retry-delay")"

printf '\n== ticking continues while the planner is gone ==\n'
reset 99
total=0; tries=0
for _ in 1 2 3 4; do
  br "$REL"; total=$(( total + $(ticks) )); tries=$(( tries + $(grep -c '^agent prompt' "$CALLS") ))
done
[ "$total" -ge 4 ] && ok "every pass still ticks the run" || nope "ticks: $total"
[ "$tries" = 1 ] && ok "the planner is tried once, not on every tick" || nope "send attempts: $tries"
[ "$(sent)" = 0 ] && ok "and nothing reaches the planner" || nope "sent $(sent)"
OUTN=$(env -i PATH="$TMP/bin:/usr/bin:/bin" HOME="$HOME" RUN_ID="$RUN" TANDEM_REAL="$TMP/tandem-real" CALLS="$CALLS" \
        STATUS_OUT="$STATUS_OUT" "$ROOT_DIR/bin/tandem-pause-note" --root "$B" 2>&1) || true
has "tandem-pause-note still prints the note" "$OUTN" "What to do:"

printf '\n== a new pause gets its own note ==\n'
reset 0
TICK_OUT="$RUN|paused|stuck"; br "$REL"
TICK_OUT="$RUN|running"; br "$REL"
[ ! -e "$RD/pause-reported" ] && ok "a running run clears the mark" || nope "mark remains"
STATUS_OUT='paused|review_unknown'; TICK_OUT="$RUN|paused|review_unknown"; br "$REL"
[ "$(sent)" = 2 ] && has "a different code after a resume is delivered" "$(cat "$PROMPTS/1")" "no valid review result" || nope "sent $(sent)"

printf '\n== a running run clears the retry files ==\n'
reset 99
STATUS_OUT='paused|stuck'; TICK_OUT="$RUN|paused|stuck"; br "$REL"
[ -f "$RD/pause-retry-at" ] && ok "the failed note left a retry time" || nope "no retry time"
TICK_OUT="$RUN|running"; br "$REL"
[ ! -e "$RD/pause-retry-at" ] && [ ! -e "$RD/pause-retry-delay" ] && [ ! -e "$RD/pause-reported" ] && ok "a running run clears all three files" || nope "files remain: $(ls "$RD")"

printf '\n== a damaged retry file does not stop the bridge ==\n'
# The wait is read from a file, so a value that is not a plain number must not
# reach the arithmetic, where "abc" or "08" would abort the loop under set -e.
STATUS_OUT='paused|stuck'; TICK_OUT="$RUN|paused|stuck"
for bad in abc 08 -5 'a[0]' ''; do
  reset 99
  br "$REL"; printf '%s\t%s\n' "$bad" "$RUN stuck" > "$RD/pause-retry-delay"; due; br "$REL"
  [ "$RC" -eq 0 ] && [ "$(cut -f1 "$RD/pause-retry-delay")" = 30 ] \
    && ok "a retry wait of '$bad' is dropped and the wait starts again at 30" || nope "wait '$bad': rc $RC, delay $(cat "$RD/pause-retry-delay"): $OUT"
done
reset 99
br "$REL"; echo abc > "$RD/pause-retry-at"; br "$REL"
[ "$RC" -eq 0 ] && [[ "$(cat "$RD/pause-retry-at")" =~ ^[0-9]+$ ]] && ok "a retry time that is not a number is ignored and replaced" || nope "retry time: rc $RC, $(cat "$RD/pause-retry-at")"

printf '\n== a changed code is not held back by the old retry time ==\n'
reset 1
STATUS_OUT='paused|stuck'; TICK_OUT="$RUN|paused|stuck"; br "$REL"
STATUS_OUT='paused|review_unknown'; TICK_OUT="$RUN|paused|review_unknown"; br "$REL"
[ "$(sent)" = 1 ] && ok "a different code is sent at once" || nope "sent $(sent)"

printf '\ntests/test-pause-note-retry: passed %d, failed %d\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

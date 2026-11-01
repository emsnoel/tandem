#!/usr/bin/env bash
# A prompt whose turn ends as soon as it starts was not delivered.
#
# A pane on a rejected model takes the prompt, goes working and is idle again
# a second later. tandem-review and tandem-handoff must report that, not exit 0.
#
# Runs the real bin/tandem-review, bin/tandem-handoff and tandem binary with
# the fake Herdr. Only handoff steps unrelated to delivery are stubbed.
set -uo pipefail
. "$(cd "$(dirname "$0")" && pwd)/lib/records.sh"
ROOT_DIR=$(cd "$(dirname "$0")/.." && pwd)

PASS=0; FAIL=0
ok()    { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope()  { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }
has()   { case "$2" in *"$3"*) ok "$1" ;; *) nope "$1 (missing: $3)" ;; esac; }
hasno() { case "$2" in *"$3"*) nope "$1 (present: $3)" ;; *) ok "$1" ;; esac; }

command -v jq >/dev/null || { echo "need jq" >&2; exit 2; }
TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
mkdir -p "$TMP/bin" "$TMP/hbin"
(cd "$ROOT_DIR" && go build -o "$TMP/bin/tandem" ./cmd/tandem) || { echo "could not build tandem"; exit 1; }
# A jq that fails: tandem-review, tandem-handoff and the fake Herdr make no jq
# call, so any call is a bug.
printf '#!/bin/sh\necho "jq was called: $*" >&2\nexit 99\n' > "$TMP/bin/jq"
chmod +x "$TMP/bin/jq"
. "$ROOT_DIR/tests/lib/fake-herdr.sh"
. "$ROOT_DIR/tests/lib/fake-socket.sh"
FAKE="$TMP/fake"
fake_herdr_install "$TMP/bin" "$FAKE"
FAKE="$FAKE" PATH="$TMP/bin:$PATH" fake_socket_start "$TMP" || exit 1

REPO="$TMP/repo"
mkdir -p "$REPO/docs/plans" "$REPO/.tandem-relay"; git -C "$REPO" init -q
REPO=$(cd "$REPO" && pwd -P); MB="$REPO/.tandem-relay"
printf '.tandem-relay/\n' > "$REPO/.gitignore"
printf '{"plans_dir":"docs/plans","check_command":"true","task_input_budget":3000000}\n' > "$REPO/.tandem.json"
PLAN="$REPO/docs/plans/p.md"
printf '# plan\n\n### Task 1: one\n\nstep\n\n### Task 2: two\n\nstep\n' > "$PLAN"
write_builder "$MB" claude
# A plan review goes to the pair's reviewer.
write_reviewer "$MB" codex

MODEL_ERROR="There's an issue with the selected model (sonnet-5.5). It may not exist or you may not have access to it."

# A fresh idle pane. ends_at_once makes the next turn fail at once.
fresh() {
  rm -f "$FAKE/prompts" "$FAKE/resets" "$FAKE"/prompt.*.txt "$FAKE/end_after" "$FAKE/gets" "$FAKE/pane" "$TMP/build-calls"
  fake_set_state idle
}
ends_at_once() { printf '2\n' > "$FAKE/end_after"; printf '%s\n' "$MODEL_ERROR" > "$FAKE/pane"; }

# Short, so the suite does not wait 8 seconds per delivery.
export TANDEM_FAIL_WITHIN_MS=1000

# A turn that ends at once failed only if its log shows no model work. The fake
# agents are Codex, so tandem finds the conversation by a delivery marker in
# the prompt. The fake logs prompts with no token counts.
export CODEX_HOME="$TMP/codex"

review() {
  OUT=$(cd "$REPO" && PATH="$TMP/bin:$ROOT_DIR/bin:/usr/bin:/bin" \
        HERDR_ENV=1 FAKE="$FAKE" REPO="$REPO" tandem-review "$PLAN" "$@" 2>&1); RC=$?
}

printf '== tandem-review: the turn keeps running ==\n'
fresh; review
[ "$RC" -eq 0 ] && ok "exits 0" || nope "exits 0 (rc $RC: $OUT)"
has "says the review was requested" "$OUT" "review requested (reviewer now working)"

printf '\n== tandem-review: the turn ends at once ==\n'
fresh; ends_at_once; review
[ "$RC" -eq 8 ] && ok "exits 8" || nope "exits 8 (rc $RC)"
hasno "does not say the review was requested" "$OUT" "review requested"
has "says the turn ended at once" "$OUT" "ended as soon as it started"
has "shows what the pane shows" "$OUT" "$MODEL_ERROR"

# tandem-handoff with the gate, the reset and the session lookup stubbed. Other
# calls go to the real binary and the fake Herdr. STUB_BUILD_FIELDS is a legacy
# build paused at a task's review, as build start's fields separated by "|".
cat > "$TMP/hbin/tandem" <<'STUB'
#!/usr/bin/env bash
case "$1 $2" in
  "gate review")         exit 0 ;;
  "agent reset")         printf 'reset\n'; exit 0 ;;
  "agent explain-reset") printf 'fresh conversation for the build (reset).\n'; exit 0 ;;
  "agent session")       case "$*" in *--not*) printf 's2\n' ;; *) printf 's1\n' ;; esac; exit 0 ;;
  "build start")         if [ -n "${STUB_BUILD_FIELDS:-}" ]; then printf '%s' "$STUB_BUILD_FIELDS" | tr '|' '\000'; exit 0; fi ;;
  "build sent"|"build review-sent") printf '%s\n' "$2" >> "$BUILD_CALLS" ;;
esac
exec "$TANDEM_REAL" "$@"
STUB
chmod +x "$TMP/hbin/tandem"
handoff() {
  rm -rf "$MB/build"
  OUT=$(cd "$REPO" && PATH="$TMP/hbin:$TMP/bin:$ROOT_DIR/bin:/usr/bin:/bin" \
        HERDR_ENV=1 FAKE="$FAKE" REPO="$REPO" TANDEM_REAL="$TMP/bin/tandem" BUILD_CALLS="$TMP/build-calls" \
        tandem-handoff "$PLAN" "$@" 2>&1); RC=$?
}

printf '\n== tandem-handoff: the turn keeps running ==\n'
fresh; handoff
[ "$RC" -eq 0 ] && ok "exits 0" || nope "exits 0 (rc $RC: $OUT)"
has "says the plan was handed over" "$OUT" "plan handed to 'builder' (now working)"
grep -qx sent "$TMP/build-calls" 2>/dev/null && ok "records the task as sent" || nope "records the task as sent"

printf '\n== tandem-handoff: the turn ends at once ==\n'
fresh; ends_at_once; handoff
[ "$RC" -eq 8 ] && ok "exits 8" || nope "exits 8 (rc $RC)"
hasno "does not say the plan was handed over" "$OUT" "plan handed to"
has "says the turn ended at once" "$OUT" "ended as soon as it started"
has "shows what the pane shows" "$OUT" "$MODEL_ERROR"
grep -qx sent "$TMP/build-calls" 2>/dev/null && nope "records the task as sent" || ok "does not record the task as sent"

printf '\n== tandem-handoff: a paused review whose turn ends at once ==\n'
printf 'Review task 1.\n' > "$TMP/review-prompt.md"
# engine run_id dry_run task total title brief ledger resumed replaced judge
# reviewer review_pending review_prompt
STUB_BUILD_FIELDS="|||1|2|one|step|||||reviewer|true|$TMP/review-prompt.md|"
export STUB_BUILD_FIELDS
fresh; handoff
[ "$RC" -eq 0 ] && ok "a review that keeps running exits 0" || nope "a review that keeps running exits 0 (rc $RC: $OUT)"
grep -qx review-sent "$TMP/build-calls" 2>/dev/null && ok "and is recorded as sent" || nope "and is recorded as sent"
fresh; ends_at_once; handoff
[ "$RC" -eq 8 ] && ok "a review whose turn ends at once exits 8" || nope "a review whose turn ends at once exits 8 (rc $RC)"
has "shows what the pane shows" "$OUT" "$MODEL_ERROR"
grep -qx review-sent "$TMP/build-calls" 2>/dev/null && nope "is recorded as sent" || ok "stays pending, so a re-run sends it again"
unset STUB_BUILD_FIELDS

printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"
(( FAIL == 0 ))

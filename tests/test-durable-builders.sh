#!/usr/bin/env bash
# The builder conformance matrix: Codex, Claude, Copilot and OpenCode in both
# engines, through the shipped binary and a fake Herdr. No model is called.
#
# Every cell is asserted from what tandem typed, to whom, and what it recorded.
# A cell that cannot hold is named with its reason in the final matrix. No live
# CLI runs here (see guide/durable-runner-operations.md for the live smoke).
set -uo pipefail
. "$(cd "$(dirname "$0")" && pwd)/lib/records.sh"
ROOT_DIR=$(cd "$(dirname "$0")/.." && pwd)

PASS=0; FAIL=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }

command -v jq >/dev/null || { echo "need jq" >&2; exit 2; }
TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
(cd "$ROOT_DIR" && go build -o "$TMP/bin/tandem" ./cmd/tandem) || { echo "could not build tandem"; exit 1; }
ln -s "$(command -v jq)" "$TMP/bin/jq"
. "$ROOT_DIR/tests/lib/fake-herdr.sh"
. "$ROOT_DIR/tests/lib/fake-socket.sh"
FAKE="$TMP/fake"
fake_herdr_install "$TMP/bin" "$FAKE"
FAKE="$FAKE" PATH="$TMP/bin:$PATH" fake_socket_start "$TMP" || exit 1
mkdir -p "$TMP/codex-home/sessions" "$TMP/copilot-home"
export CODEX_HOME="$TMP/codex-home" COPILOT_HOME="$TMP/copilot-home"

KINDS="codex claude copilot opencode"
# Kinds with a verified reset. The others get a task only when the run
# accepts a shared conversation.
resets() { case "$1" in codex|claude|copilot) return 0 ;; *) return 1 ;; esac; }
MATRIX=""
cell() { MATRIX="$MATRIX$1|$2|$3|$4
"; }  # cell KIND MODE OPERATION RESULT

mkrepo() { # mkrepo DIR KIND
  rm -rf "$1"; mkdir -p "$1/.tandem-relay"; git -C "$1" init -q
  git -C "$1" config user.email t@t; git -C "$1" config user.name t
  printf '.tandem-relay/\n.tandem-history/\n' > "$1/.gitignore"
  write_builder "$1/.tandem-relay" "$2"
  printf '# Plan\n\n**Goal:** g.\n\n### Task 1: First\n\nfirst thing\n\n### Task 2: Second\n\nsecond thing\n' > "$1/plan.md"
  git -C "$1" add . && git -C "$1" commit -qm plan
}
reset_fake() { rm -f "$FAKE"/prompts "$FAKE"/prompt.*.txt "$FAKE"/resets "$FAKE"/kind "$FAKE"/session.*; fake_set_state idle 10; printf 'sess-1\n' > "$FAKE/session"; }
nprompts() { [ -f "$FAKE/prompts" ] && wc -l < "$FAKE/prompts" | tr -d ' ' || echo 0; }
nresets() { [ -f "$FAKE/resets" ] && wc -l < "$FAKE/resets" | tr -d ' ' || echo 0; }
field() { printf '%s' "$1" | grep -o -- "--$2 [0-9a-f]*" | head -1 | cut -d' ' -f2; }
jfield() { printf '%s' "$1" | grep -o "\"$2\":\"[0-9a-f]*\"" | head -1 | cut -d'"' -f4; }

# ---------------------------------------------------------------------------
printf '== TestDurableBuilderConformance ==\n'
for K in $KINDS; do
  printf '\n-- %s, durable --\n' "$K"
  R="$TMP/d-$K"; mkrepo "$R" "$K"; R=$(cd "$R" && pwd -P)
  reset_fake; [ "$K" = copilot ] && printf 'copilot\n' > "$FAKE/kind"
  td() { env PATH="$TMP/bin:/usr/bin:/bin" FAKE="$FAKE" REPO="$R" CODEX_HOME="$CODEX_HOME" COPILOT_HOME="$COPILOT_HOME" TANDEM_BUILD_ENGINE=durable TANDEM_STATE_HOME="$TMP/state-d-$K" tandem "$@"; }
  share=""; resets "$K" || share="--share-context"

  # Launch: the kind is accepted with its flags. A kind with no verified reset is
  # refused a task until the run accepts a shared conversation.
  if ! resets "$K"; then
    td build start --plan "$R/plan.md" --root "$R" --builder builder --planner planner --builder-kind "$K" --reviewer reviewer --reviewer-kind claude --check true >/dev/null 2>&1
    td build tick --root "$R" >/dev/null 2>&1
    if [ "$(nprompts)" -eq 0 ] && [ "$(nresets)" -eq 0 ]; then
      ok "$K: with no verified reset nothing is typed into its unknown conversation"; cell "$K" durable "fresh task (strict)" "refused, nothing typed"
    else nope "$K: typed into an unverified conversation"; cell "$K" durable "fresh task (strict)" FAIL; fi
    rm -rf "$TMP/state-d-$K" "$R/.tandem-relay/active-run.json" "$R/.tandem-relay/durable"; reset_fake
  fi
  OUT=$(td build start --plan "$R/plan.md" --root "$R" --builder builder --planner planner --builder-kind "$K" --reviewer reviewer --reviewer-kind claude --check true $share 2>&1); rc=$?
  RUN=$(printf '%s' "$OUT" | jq -r .run_id 2>/dev/null)
  if [ "$rc" -eq 0 ] && [[ "$RUN" =~ ^[0-9a-f]{32}$ ]]; then ok "$K: launch (a durable run starts with this kind)"; cell "$K" durable launch pass
  else nope "$K: start rc $rc: $OUT"; cell "$K" durable launch FAIL; continue; fi
  [ "$(td build status --root "$R" --json | jq -r .state)" = running ] || nope "$K: status"

  # Fresh task, acknowledgement.
  tickn() { for _ in 1 2 3 4; do td build tick --root "$R" >/dev/null 2>&1; done; }
  tickn
  PL=$(sed -n 1p "$FAKE/prompt.1.txt" 2>/dev/null | head -c 0; cat "$FAKE/prompt.1.txt" 2>/dev/null)
  ATT=$(field "$PL" attempt); JOB=$(field "$PL" job); EFF=$(field "$PL" effect); NONCE=$(field "$PL" nonce)
  if resets "$K"; then
    [ "$(nresets)" -eq 1 ] && ok "$K: fresh task (one reset before the prompt)" && cell "$K" durable "fresh task" "reset, then prompt" || { nope "$K: resets $(nresets)"; cell "$K" durable "fresh task" FAIL; }
  else
    [ "$(nresets)" -eq 0 ] && [ "$(nprompts)" -eq 1 ] && ok "$K: fresh task (shared conversation, accepted by the run, no reset guessed)" && cell "$K" durable "fresh task" "shared (--share-context), no reset" || { nope "$K: shared task: resets $(nresets) prompts $(nprompts)"; cell "$K" durable "fresh task" FAIL; }
  fi
  [ -n "$ATT" ] && [ -n "$NONCE" ] || { nope "$K: the prompt carries no identity"; continue; }
  BAD=$(printf '%s' "$NONCE" | rev)
  td build event new --root "$R" --kind builder_started --task 1 --attempt "$ATT" --job "$JOB" --effect "$EFF" --nonce "$BAD" >/dev/null 2>&1
  rc1=$?
  td build event new --root "$R" --kind builder_started --task 1 --attempt "$ATT" --job "$JOB" --effect "$EFF" --nonce "$NONCE" >/dev/null 2>&1; rc2=$?
  tickn
  if [ "$rc1" -ne 0 ] || [ "$(td build status --root "$R" --json | jq -r '.state')" = running ]; then
    [ "$rc2" -eq 0 ] && ok "$K: start acknowledgement binds to this attempt's nonce" && cell "$K" durable "start ack" pass || { nope "$K: the right nonce was refused"; cell "$K" durable "start ack" FAIL; }
  fi

  # Submit, check, review, accept.
  echo "work $K" > "$R/file1.txt"
  printf 'done: task 1\n' > "$TMP/report-$K.md"
  td build event new --root "$R" --kind builder_submitted --task 1 --attempt "$ATT" --job "$JOB" --effect "$EFF" --nonce "$NONCE" --payload "$TMP/report-$K.md" >/dev/null 2>&1 \
    && ok "$K: submit (explicit result for the current job)" && cell "$K" durable submit pass || { nope "$K: submit refused"; cell "$K" durable submit FAIL; }
  fake_set_state idle; tickn
  RV=$(grep -l '"job_id"' "$FAKE"/prompt.*.txt 2>/dev/null | head -1)
  if [ -n "$RV" ]; then ok "$K: the controller ran the check and sent the review prompt"; cell "$K" durable "check" pass
  else nope "$K: no review prompt after submit"; cell "$K" durable check FAIL; continue; fi
  RVT=$(cat "$RV")
  RJ=$(jfield "$RVT" job_id); RA=$(jfield "$RVT" attempt_id); RP=$(jfield "$RVT" patch_hash); RT=$(jfield "$RVT" tree_hash)
  printf '{"schema_version":1,"job_id":"%s","attempt_id":"%s","patch_hash":"%s","tree_hash":"%s","status":"changes_requested","findings":[{"severity":"high","path":"file1.txt","disposition":"open","message":"fix it","start_line":null,"end_line":null}]}\n' "$RJ" "$RA" "$RP" "$RT" > "$TMP/review-$K-1.json"
  fake_set_state working
  td build review-record --root "$R" --result "$TMP/review-$K-1.json" >/dev/null 2>"$TMP/err"
  if [ $? -ne 0 ] && grep -q 'not finished' "$TMP/err"; then ok "$K: a review is refused while the reviewer pane is working (uncertainty is visible)"; cell "$K" durable "reviewer pane review" "refused until settled"
  else nope "$K: review accepted from a working pane"; cell "$K" durable "reviewer pane review" FAIL; fi
  fake_set_state idle
  td build review-record --root "$R" --result "$TMP/review-$K-1.json" >/dev/null 2>&1 && ok "$K: reviewer pane review recorded once the reviewer pane settled" || nope "$K: review-record failed"

  # Repair: a new attempt, and the old attempt's events are rejected.
  N0=$(nprompts); tickn
  RP2=$(grep -l 'Implement task 1' "$FAKE"/prompt.*.txt | tail -1)
  ATT2=$(field "$(cat "$RP2")" attempt)
  if [ "$(nprompts)" -gt "$N0" ] && [ -n "$ATT2" ] && [ "$ATT2" != "$ATT" ]; then ok "$K: repair starts a new attempt"; cell "$K" durable repair "new attempt"
  else nope "$K: no repair attempt"; cell "$K" durable repair FAIL; fi
  # The queue takes the old attempt's message; ingestion quarantines it.
  Q0=$(ls "$R/.tandem-relay/durable/$RUN/quarantine" 2>/dev/null | grep -c '\.reason$')
  td build event new --root "$R" --kind builder_submitted --task 1 --attempt "$ATT" --job "$JOB" --effect "$EFF" --nonce "$NONCE" --payload "$TMP/report-$K.md" >/dev/null 2>&1
  tickn
  Q1=$(ls "$R/.tandem-relay/durable/$RUN/quarantine" 2>/dev/null | grep -c '\.reason$')
  if [ "$Q1" -gt "$Q0" ]; then ok "$K: an event for the superseded attempt is quarantined with a reason"; cell "$K" durable "stale event" quarantined
  else nope "$K: the superseded attempt's event was not quarantined"; cell "$K" durable "stale event" FAIL; fi

  # Restart: recovery sends nothing twice.
  N1=$(nprompts)
  td build recover --root "$R" >/dev/null 2>&1; td build recover --root "$R" >/dev/null 2>&1
  [ "$(nprompts)" -eq "$N1" ] && ok "$K: restart (recover) resends nothing" && cell "$K" durable restart "nothing resent" || { nope "$K: recover resent a prompt"; cell "$K" durable restart FAIL; }

  # Usage: no readable log means unknown, never zero.
  U=$(td build usage --root "$R" --json 2>/dev/null)
  if [ "$(printf '%s' "$U" | jq -r '.completeness // .total.completeness // "x"')" != complete ] && [ "$(printf '%s' "$U" | jq -r '(.input // .total.input) // "null"')" != 0 ]; then
    ok "$K: usage with no readable log is unavailable, not zero"; cell "$K" durable "usage unavailable" "unknown, not zero"
  else nope "$K: usage $U"; cell "$K" durable "usage unavailable" FAIL; fi

  # Stop: the stop notice is typed once, and the writer is released only after
  # the builder is seen idle.
  fake_set_state working
  N2=$(nprompts)
  td build stop --root "$R" >/dev/null 2>&1; tickn
  ST=$(td build status --root "$R" --json | jq -r .state)
  fake_set_state idle; tickn
  ST2=$(td build status --root "$R" --json | jq -r .state)
  if [ "$ST" = stopping ] && [ "$ST2" = paused ]; then ok "$K: stop waits for the builder to go idle before releasing the writer"; cell "$K" durable stop "released after idle"
  else nope "$K: stop went $ST then $ST2 (prompts $N2 -> $(nprompts))"; cell "$K" durable stop FAIL; fi
done

# ---------------------------------------------------------------------------
printf '\n== TestLegacyBuilderConformance ==\n'
for K in $KINDS; do
  printf '\n-- %s, legacy --\n' "$K"
  R="$TMP/l-$K"; mkrepo "$R" "$K"; R=$(cd "$R" && pwd -P)
  # A legacy build reviews in the pair's reviewer pane; the builder never does.
  write_reviewer "$R/.tandem-relay" codex
  reset_fake
  # The reviewer pane runs its own conversation, never the builder's.
  printf 'sess-r\n' > "$FAKE/session.reviewer"
  tl() { env PATH="$TMP/bin:/usr/bin:/bin" FAKE="$FAKE" REPO="$R" CODEX_HOME="$CODEX_HOME" TANDEM_STATE_HOME="$TMP/state-l-$K" tandem "$@"; }
  OUT=$(tl build start --plan "$R/plan.md" --root "$R" --builder builder --planner planner --check true 2>&1)
  if [ "$(printf '%s' "$OUT" | jq -r '"\(.task)/\(.total)"' 2>/dev/null)" = "1/2" ] && [ ! -f "$R/.tandem-relay/active-run.json" ]; then
    ok "$K: legacy launch (a build record, no durable marker)"; cell "$K" legacy launch pass
    [ "$(jq -r .reviewer "$R/.tandem-relay/build/build.json")" = reviewer ] && ok "$K: the build records the reviewer pane" || nope "$K: the build records no reviewer pane"
  else nope "$K: legacy start: $OUT"; cell "$K" legacy launch FAIL; continue; fi
  # Fresh task: the legacy handoff asks `tandem agent explain-reset`.
  EX=$(tl agent explain-reset "$K" --for build 2>&1)
  if resets "$K"; then
    case "$EX" in *"fresh conversation"*|*reset*|*new*) ok "$K: fresh task (an adapter reset exists)"; cell "$K" legacy "fresh task" "adapter reset" ;; *) nope "$K: explain-reset: $EX"; cell "$K" legacy "fresh task" FAIL ;; esac
  else
    case "$EX" in *"fresh conversation"*) nope "$K: claims a fresh conversation it cannot start: $EX"; cell "$K" legacy "fresh task" FAIL ;; *) ok "$K: fresh task (no verified reset, said plainly)"; cell "$K" legacy "fresh task" "no verified reset, shared" ;; esac
  fi
  tl build sent --root "$R" --task 1 >/dev/null 2>&1
  fake_set_state idle
  RED=$(env PATH="$TMP/bin:/usr/bin:/bin" FAKE="$FAKE" REPO="$R" CODEX_HOME="$CODEX_HOME" tandem build advance --root "$R" --task 1 2>&1)
  # Every task is reviewed in the reviewer pane, which passes it here.
  case "$RED" in "review 1 "*)
    RED=$(env PATH="$TMP/bin:/usr/bin:/bin" FAKE="$FAKE" REPO="$R" CODEX_HOME="$CODEX_HOME" tandem build advance --root "$R" --task 1 --event review-1 --review --verdict clean --from reviewer 2>&1) ;;
  esac
  case "$RED" in "next 2"*) ok "$K: handback of task 1 is checked, reviewed, and task 2 is next"; cell "$K" legacy "submit + check" pass ;; *) nope "$K: advance: $RED"; cell "$K" legacy "submit + check" FAIL ;; esac
  [ "$(jq -r '.tasks[0].status' "$R/.tandem-relay/build/build.json")" = done ] && ok "$K: the record shows task 1 done" || nope "$K: record"
  tl build stop --root "$R" --reason test >/dev/null 2>&1
  [ "$(jq -r .state "$R/.tandem-relay/build/build.json")" = stopped ] && ok "$K: stop is recorded" && cell "$K" legacy stop "recorded" || { nope "$K: stop"; cell "$K" legacy stop FAIL; }
  OUT=$(tl build start --plan "$R/plan.md" --root "$R" --builder builder --planner planner --check true 2>&1)
  if [ "$(printf '%s' "$OUT" | jq -r '"\(.task) \(.resumed)"' 2>/dev/null)" = "2 true" ]; then ok "$K: restart resumes at task 2 and repeats nothing"; cell "$K" legacy restart "resumes at 2"
  else nope "$K: restart: $OUT"; cell "$K" legacy restart FAIL; fi
done

printf '\n== matrix (fixture results; live verification: none, every kind UNVERIFIED live) ==\n'
printf '%s' "$MATRIX" | awk -F'|' 'NF==4 { printf "  %-9s %-8s %-22s %s\n", $1, $2, $3, $4 }'

printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

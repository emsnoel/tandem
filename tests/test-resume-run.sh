#!/usr/bin/env bash
# Resuming a stopped run at the task it stopped on, with the real binary.
#
#   - A run stopped after task 2 of 3 is accepted is archived. tandem-handoff
#     --resume starts a new run of the same plan at task 3, and types no
#     prompt for tasks 1 or 2.
#   - tandem-unpair signs off all three tasks: two on the evidence carried
#     over from the first run, one on the new run's own.
#   - A tree that no longer matches what the run left refuses the resume and
#     creates nothing. So does a checkout with no archived run of the plan.
#   - The plan's review gate still holds for a resume.
#   - A task that was mid-repair resumes with its open findings in the prompt.
#
# Herdr is the fake one; `tandem` is the binary built from this tree, with
# only the pane lookups and the plan review gate stubbed.
set -uo pipefail
. "$(cd "$(dirname "$0")" && pwd)/lib/records.sh"
ROOT_DIR=$(cd "$(dirname "$0")/.." && pwd)

PASS=0; FAIL=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }
has()  { case "$2" in *"$3"*) ok "$1" ;; *) nope "$1 (missing: $3) in: $(printf "%s" "$2" | head -c 600)" ;; esac; }

command -v jq >/dev/null || { echo "need jq" >&2; exit 2; }
TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
mkdir -p "$TMP/bin"
(cd "$ROOT_DIR" && go build -o "$TMP/tandem-real" ./cmd/tandem) || { echo "could not build tandem"; exit 1; }
. "$ROOT_DIR/tests/lib/fake-herdr.sh"
. "$ROOT_DIR/tests/lib/fake-socket.sh"
fake_herdr_install "$TMP/bin" "$TMP/fake"
FAKE="$TMP/fake" PATH="$TMP/bin:$PATH" fake_socket_start "$TMP" || exit 1
# Every agent has a live, idle pane of its own. GATE_RC refuses the plan's
# review gate; by default the plan has cleared review.
cat > "$TMP/bin/tandem" <<'STUB'
#!/usr/bin/env bash
case "$1 $2" in
  "agent snapshot") printf 'w1:%s\t%s\tidle\n' "$3" "$REPO"; exit 0 ;;
  "agent status")   printf 'idle\n'; exit 0 ;;
  "gate review")    [ "${GATE_RC:-0}" = 0 ] && { printf 'reviewed\n'; exit 0; }
                    printf 'absent\n'; exit "$GATE_RC" ;;
esac
case "$1" in
  pane) exit 0 ;;
esac
exec "$TANDEM_REAL" "$@"
STUB
printf '#!/usr/bin/env bash\nexit 0\n' > "$TMP/bin/tandem-review"
chmod +x "$TMP/bin/tandem" "$TMP/bin/tandem-review"

export TANDEM_REAL="$TMP/tandem-real" TANDEM_STATE_HOME="$TMP/state" FAKE="$TMP/fake" HERDR_ENV=1
export TANDEM_DISPATCH_WAIT_S=5
PATHX="$TMP/bin:$ROOT_DIR/bin:/usr/bin:/bin"

mkrepo() { # mkrepo DIR: a committed repo with a three-task plan, a codex builder and a claude reviewer
  rm -rf "$1"; mkdir -p "$1/.tandem-relay"; git -C "$1" init -q
  git -C "$1" config user.email t@t; git -C "$1" config user.name t
  printf '.tandem-relay/\n' > "$1/.gitignore"
  printf '{"check_command":"true","task_input_budget":0}\n' > "$1/.tandem.json"
  write_builder "$1/.tandem-relay"; write_reviewer "$1/.tandem-relay"
  printf '# Plan\n\n**Goal:** g.\n\n### Task 1: First\n\nfirst thing\n\n### Task 2: Second\n\nsecond thing\n\n### Task 3: Third\n\nthird thing\n' > "$1/plan.md"
  git -C "$1" add . && git -C "$1" commit -qm plan
}
td() { env PATH="$PATHX" REPO="$R" "$@"; }
handoff() { (cd "$R" && env PATH="$PATHX" REPO="$R" tandem-handoff "$R/plan.md" "$@" 2>&1); }
unpair() { (cd "$R" && env PATH="$PATHX" REPO="$R" TANDEM_UNPAIR_SETTLE_POLLS=3 tandem-unpair "$@" 2>&1); }
run_id() { jq -r .run_id "$R/.tandem-relay/active-run.json" 2>/dev/null; }

# The identity of the newest task prompt, from the fake pane's log.
P=""; ATT=""; JOB=""; EFF=""; NONCE=""
last_prompt() {
  local line
  line=$(grep -n 'Implement task' "$FAKE/prompts" | tail -1 | cut -d: -f1); P=$(cat "$FAKE/prompt.$line.txt")
  ATT=$(builder_prompt_field "$P" attempt)
  JOB=$(builder_prompt_field "$P" job)
  EFF=$(builder_prompt_field "$P" effect)
  NONCE=$(builder_prompt_field "$P" nonce)
}
# builder_event N KIND [PAYLOAD]: the builder's acknowledgement or handback for task N.
builder_event() {
  local extra=(); [ -n "${3:-}" ] && extra=(--payload "$3")
  td tandem build event new --root "$R" --kind "$2" --task "$1" --attempt "$ATT" --job "$JOB" --effect "$EFF" --nonce "$NONCE" ${extra[@]+"${extra[@]}"} >/dev/null 2>&1
}
# build_task N: the builder starts and hands back task N, and the check and the
# review prompt follow.
build_task() {
  fake_set_state idle; td tandem build tick --root "$R" >/dev/null 2>&1
  last_prompt
  builder_event "$1" builder_started
  printf 'task %s\n' "$1" > "$R/work$1.txt"; printf 'done\n' > "$TMP/report.md"
  builder_event "$1" builder_submitted "$TMP/report.md"
  fake_set_state idle; td tandem build tick --root "$R" >/dev/null 2>&1
}
# review STATUS [FINDINGS]: the reviewer's reply to the newest review prompt.
review() {
  local line p rj rp rt
  line=$(grep -n '"job_id"' "$FAKE/prompts" | tail -1 | cut -d: -f1); p=$(cat "$FAKE/prompt.$line.txt")
  rj=$(printf '%s' "$p" | grep -o '"job_id":"[0-9a-f]*"' | head -1 | cut -d'"' -f4)
  rp=$(printf '%s' "$p" | grep -o '"patch_hash":"[0-9a-f]*"' | head -1 | cut -d'"' -f4)
  rt=$(printf '%s' "$p" | grep -o '"tree_hash":"[0-9a-f]*"' | head -1 | cut -d'"' -f4)
  printf '{"schema_version":1,"job_id":"%s","attempt_id":"%s","patch_hash":"%s","tree_hash":"%s","status":"%s","findings":%s}\n' \
    "$rj" "$ATT" "$rp" "$rt" "$1" "${2:-[]}" > "$TMP/review.json"
  fake_set_state idle; td tandem build review-record --root "$R" --result "$TMP/review.json" >/dev/null 2>&1
  fake_set_state idle; td tandem build tick --root "$R" >/dev/null 2>&1
}
# finish_task N: build task N and pass its review, so the next tick accepts it.
finish_task() { build_task "$1"; review passed; }
# stop_and_archive RUN: the builder acknowledges the prompt it was sent, then
# the run is stopped, its open jobs ended and the run archived.
stop_and_archive() {
  last_prompt; builder_event "$2" builder_started
  td tandem build stop --root "$R" >/dev/null 2>&1
  # The stop notice typed into the pane starts a turn, which then ends.
  td tandem build tick --root "$R" >/dev/null 2>&1; fake_set_state idle
  td tandem build wait --root "$R" --until paused --tick --timeout 5000 >/dev/null 2>&1
  td tandem state recover --run "$1" --root "$R" >/dev/null 2>&1
  td tandem state archive --run "$1" --root "$R" >/dev/null 2>"$TMP/err"
}
task_prompts() { grep -o 'Implement task [0-9]* of [0-9]*' "$FAKE/prompts" 2>/dev/null; }

printf '\n== a run stopped after task 2 of 3 resumes at task 3 ==\n'
R="$TMP/repo"; mkrepo "$R"; R=$(cd "$R" && pwd -P); : > "$FAKE/prompts"; fake_set_state idle
OUT=$(handoff); rc=$?
RUN_A=$(run_id)
[ "$rc" -eq 0 ] && [[ "$RUN_A" =~ ^[0-9a-f]{32}$ ]] && ok "the first handoff starts run $RUN_A" || { nope "first handoff rc $rc: $OUT"; exit 1; }
finish_task 1; finish_task 2
ST=$(td tandem build status --root "$R" --json | jq -r '[.tasks[] | select(.accepted) | .id] | join(",")')
[ "$ST" = "task-1,task-2" ] && ok "setup: tasks 1 and 2 are accepted" || nope "setup: accepted $ST"
stop_and_archive "$RUN_A" 3 && ok "setup: the run is stopped and archived" || nope "setup: archive: $(cat "$TMP/err")"

NOTE=$(td tandem-pause-note --root "$R" 2>&1)
has "with the run archived, the pause note says no run is active" "$NOTE" "no run is active"

printf 'stray\n' > "$R/stray.txt"
OUT=$(handoff --resume); rc=$?
[ "$rc" -eq 23 ] && ok "a tree the run did not leave refuses the resume (exit 23)" || nope "stray tree rc $rc: $OUT"
has "and the refusal says the files changed since task-2 was accepted" "$OUT" "task-2"
has "and that nothing was started" "$OUT" "no run was started"
[ ! -f "$R/.tandem-relay/active-run.json" ] && ok "and no run was created" || nope "a run was created: $(run_id)"
rm -f "$R/stray.txt"

OUT=$(td env GATE_RC=24 bash -c 'cd "$REPO" && tandem-handoff "$REPO/plan.md" --resume' 2>&1); rc=$?
[ "$rc" -eq 24 ] && ok "a plan that has not cleared review refuses the resume (exit 24)" || nope "gate rc $rc: $OUT"
[ ! -f "$R/.tandem-relay/active-run.json" ] && ok "and no run was created" || nope "a run was created past the gate"

BEFORE=$(task_prompts | wc -l | tr -d ' ')
OUT=$(handoff --resume); rc=$?
RUN_B=$(run_id)
[ "$rc" -eq 0 ] && [[ "$RUN_B" =~ ^[0-9a-f]{32}$ ]] && [ "$RUN_B" != "$RUN_A" ] && ok "tandem-handoff --resume starts a new run $RUN_B" || nope "resume rc $rc: $OUT"
has "and says which run it resumes" "$OUT" "resumes run $RUN_A"
NEW=$(task_prompts | tail -n +"$((BEFORE+1))")
[ "$NEW" = "Implement task 3 of 3" ] && ok "the only task prompt it types is task 3" || nope "prompts after resume: $NEW"
ST=$(td tandem build status --root "$R" --json)
[ "$(printf '%s' "$ST" | jq -r '[.tasks[] | select(.accepted) | .id] | join(",")')" = "task-1,task-2" ] \
  && ok "tasks 1 and 2 start out accepted" || nope "status: $ST"
[ "$(printf '%s' "$ST" | jq -r '[.tasks[] | .carried_from // ""] | join(",")')" = "$RUN_A,$RUN_A," ] \
  && ok "and the status says they were carried over from run $RUN_A" || nope "carried_from: $ST"
[ "$(printf '%s' "$ST" | jq -r .resumed_from)" = "$RUN_A" ] && ok "the run records what it resumed" || nope "resumed_from: $ST"

finish_task 3
ST=$(td tandem build status --root "$R" --json | jq -r '"\(.state) \([.tasks[] | select(.accepted)] | length)"')
[ "$ST" = "finished 3" ] && ok "task 3 is accepted and the run finishes" || nope "after task 3: $ST"
OUT=$(unpair); rc=$?
[ "$rc" -eq 0 ] && ok "tandem-unpair signs off all three tasks without --force" || nope "unpair rc $rc: $OUT"
has "the check ran" "$OUT" "check passed"
has "and the resumed run was archived" "$OUT" "archived run $RUN_B"

printf '\n== nothing to resume ==\n'
R="$TMP/repo2"; mkrepo "$R"; R=$(cd "$R" && pwd -P); : > "$FAKE/prompts"; fake_set_state idle
OUT=$(handoff --resume); rc=$?
[ "$rc" -eq 23 ] && ok "a checkout with no archived run of the plan refuses (exit 23)" || nope "no run rc $rc: $OUT"
has "and says there is no run to resume" "$OUT" "no archived run"
[ ! -f "$R/.tandem-relay/active-run.json" ] && ok "and no run was created" || nope "a run was created"
OUT=$(handoff --resume --continue); rc=$?
[ "$rc" -ne 0 ] && has "--resume and --continue together are refused" "$OUT" "--continue" || nope "--resume --continue rc 0"

printf '\n== a task that was mid-repair resumes with its open findings ==\n'
OUT=$(handoff); rc=$?
RUN_A=$(run_id)
[ "$rc" -eq 0 ] && ok "a run starts ($RUN_A)" || { nope "handoff rc $rc: $OUT"; exit 1; }
finish_task 1
build_task 2
review changes_requested '[{"severity":"high","path":"work2.txt","disposition":"open","message":"the greeting is missing its full stop","start_line":1,"end_line":1}]'
fake_set_state idle; td tandem build tick --root "$R" >/dev/null 2>&1
last_prompt
case "$P" in *"Open findings"*) ok "setup: the repair of task 2 was sent" ;; *) nope "setup: no repair prompt: $(td tandem build status --root "$R")" ;; esac
stop_and_archive "$RUN_A" 2 && ok "setup: the run is stopped mid-repair and archived" || nope "setup: archive: $(cat "$TMP/err")"
BEFORE=$(task_prompts | wc -l | tr -d ' ')
OUT=$(handoff --resume); rc=$?
RUN_B=$(run_id)
[ "$rc" -eq 0 ] && [ "$RUN_B" != "$RUN_A" ] && ok "the resume starts a new run" || nope "resume rc $rc: $OUT"
NEW=$(task_prompts | tail -n +"$((BEFORE+1))")
[ "$NEW" = "Implement task 2 of 3" ] && ok "it starts at task 2" || nope "prompts after resume: $NEW"
last_prompt
has "the prompt belongs to the new run" "$P" "Context: run $RUN_B"
has "it says the task resumes run $RUN_A" "$P" "resumes run $RUN_A"
has "and carries the open finding" "$P" "the greeting is missing its full stop"
has "and names the files the earlier attempt changed" "$P" "work2.txt"

printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

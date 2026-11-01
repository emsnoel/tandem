#!/usr/bin/env bash
# tandem-handoff sends one task of a plan, with its brief, in a fresh
# conversation, and tandem sends the next one itself.
#
# Herdr is stubbed. `tandem build` and `tandem agent instruction-files` run the
# real binary, so the split, the resume and the ledger are the shipped code.
set -uo pipefail
. "$(cd "$(dirname "$0")" && pwd)/lib/records.sh"
ROOT_DIR=$(cd "$(dirname "$0")/.." && pwd)
HANDOFF="$ROOT_DIR/bin/tandem-handoff"

PASS=0; FAIL=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }
flat() { printf '%s' "$1" | tr '\n' ' ' | tr -s ' '; }
has()   { case "$(flat "$2")" in *"$3"*) ok "$1" ;; *) nope "$1 (missing: $3)" ;; esac; }
hasnt() { case "$(flat "$2")" in *"$3"*) nope "$1 (present: $3)" ;; *) ok "$1" ;; esac; }

TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
REPO="$TMP/repo"; RELAY="$REPO/.tandem-relay"
mkdir -p "$TMP/bin" "$REPO/docs/plans" "$RELAY"
git -C "$REPO" init -q
printf '.tandem-relay/\n' > "$REPO/.gitignore"
PLAN="$REPO/docs/plans/p.md"
cat > "$PLAN" <<'EOF'
# Greeting fixes

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans.

**Goal:** greet properly.

### Task 1: Punctuate

Make it say Hello, World!

### Task 2: Farewell

Add farewell.sh.

### Task 3: Review

Review both.
EOF

(cd "$ROOT_DIR" && go build -o "$TMP/tandem-real" ./cmd/tandem) \
  || { echo "could not build tandem"; exit 1; }
# A jq that fails: tandem-handoff asks tandem, so any jq call is a bug.
printf '#!/bin/sh\necho "jq was called: $*" >&2\nexit 99\n' > "$TMP/bin/jq"
printf '#!/usr/bin/env bash\nexit 0\n' > "$TMP/bin/herdr"
printf '#!/usr/bin/env bash\nexit 0\n' > "$TMP/bin/tandem-review"
# Herdr calls are answered here; build, instruction-files and builder-kind go
# to the real binary. CALLS records every prompt sent.
cat > "$TMP/bin/tandem" <<'STUB'
#!/usr/bin/env bash
case "$1" in
  build) exec "$TANDEM_REAL" "$@" ;;
  paths) printf '%s\n' "$RELAY"; exit 0 ;;
esac
case "$1 $2" in
  "agent instruction-files"|"config builder-kind") exec "$TANDEM_REAL" "$@" ;;
  # root plans_dir check_command conventions_file builder_kind prompt_extra
  # task_input_budget context_limit build_engine review_delivery
  # acceptance_policy collaboration source invalid_manifest
  "config project") printf '%s\0' "$REPO" docs/plans ./check.sh "${STUB_CONV-AGENTS.md}" codex "" 3000000 0 "" "" "" "" stub ""; exit 0 ;;
  "state engine")   exec "$TANDEM_REAL" "$@" ;;
  "agent snapshot") printf '%%1\t%s\tidle\n' "$REPO"; exit 0 ;;
  "gate review")    exit 0 ;;
  "agent reset")    printf 'reset\n'; printf 'reset\n' >> "$CALLS.resets"; exit 0 ;;
  "agent explain-reset") printf 'fresh conversation for the build (reset).\n'; exit 0 ;;
  "agent session")  case "$*" in *--not*) printf 's2\n' ;; *) printf 's1\n' ;; esac; exit 0 ;;
  "agent prompt")   printf '%s\n' "$4" > "$CALLS"; printf '%s\n' "$3" > "$CALLS.target"; exit 0 ;;
  "agent status")   printf 'working\n'; exit 0 ;;
esac
exit 0
STUB
chmod +x "$TMP/bin/herdr" "$TMP/bin/tandem-review" "$TMP/bin/tandem" "$TMP/bin/jq"

kind() { write_builder "$RELAY" "$1"; }
# A build sends every review to the recorded reviewer pane.
write_reviewer "$RELAY" claude reviewer opus
run() {
  PATH="$TMP/bin:$PATH" HERDR_ENV=1 REPO="$REPO" RELAY="$RELAY" TANDEM_REAL="$TMP/tandem-real" \
    CALLS="$TMP/calls" "$HANDOFF" "$PLAN" "$@" 2>&1
}
prompt_of() { printf '%s\n' "$1" | awk '/^--- prompt that would be sent ---$/{f=1;next} /^--- end ---$/{f=0} f'; }

printf '== the first handoff sends task 1 with its brief ==\n'
kind codex
OUT=$(run --dry-run); P=$(prompt_of "$OUT")
[ -n "$P" ] && ok "the prompt rendered" || nope "the prompt rendered"
has   "names the task and the total"      "$P" "Implement task 1 of 3 of the plan at $PLAN"
has   "names the brief"                    "$P" "Your brief is $RELAY/build/task-1.md"
has   "says not to read the plan"          "$P" "Do not read the plan itself"
hasnt "no whole-plan read"                 "$P" "Read the whole plan"
hasnt "no builder-written briefs"          "$P" "IF YOU DELEGATE"
has   "hands back this task by number"     "$P" "--task 1 --report <file>"
has   "says to stop after handing back"    "$P" "Do not start the next task"
has   "runs the check through tandem"      "$P" "tandem check"
has   "tells the human which task"         "$OUT" "task 1 of 3: Punctuate"
[ -d "$RELAY/build" ] && nope "a dry run wrote no build" || ok "a dry run wrote no build"

printf '\n== the conventions file is asked for only when the CLI does not load it ==\n'
hasnt "codex is not told to read AGENTS.md" "$P" "Also read $REPO/AGENTS.md"
kind copilot
P=$(prompt_of "$(run --dry-run)")
has   "copilot is told to read AGENTS.md"  "$P" "Also read $REPO/AGENTS.md in full"
kind claude
P=$(prompt_of "$(run --dry-run)")
has   "claude is told to read AGENTS.md"   "$P" "Also read $REPO/AGENTS.md in full"
P=$(STUB_CONV=CLAUDE.md; export STUB_CONV; prompt_of "$(run --dry-run)")
hasnt "claude is not told to read CLAUDE.md" "$P" "Also read"
kind codex

printf '\n== --continue refuses when there is no build ==\n'
OUT=$(run --continue --dry-run); rc=$?
[ "$rc" -ne 0 ] && ok "refuses (rc $rc)" || nope "refuses (rc $rc)"
has "says why" "$OUT" "nothing to continue"

printf '\n== a real send starts the build and records the task ==\n'
OUT=$(run); rc=$?
[ "$rc" -eq 0 ] && ok "handoff succeeds" || nope "handoff succeeds (rc $rc): $OUT"
B="$RELAY/build/build.json"
[ -f "$RELAY/build/task-3.md" ] && ok "one brief per task" || nope "one brief per task"
[ "$(jq -r '.tasks[0].status' "$B" 2>/dev/null)" = sent ] && ok "task 1 is sent" || nope "task 1 is sent"
[ "$(jq -r '.tasks[0].session' "$B" 2>/dev/null)" = s2 ] && ok "records the conversation it went to" || nope "records the conversation it went to"
jq -e '.tasks[0].tree_before' "$B" >/dev/null 2>&1 && ok "snapshots the tree before the task" || nope "snapshots the tree before the task"
grep -q "REQUIRED SUB-SKILL" "$RELAY/build/task-1.md" && nope "the brief drops the sub-skill line" || ok "the brief drops the sub-skill line"
has "the sent prompt is task 1" "$(cat "$TMP/calls")" "Implement task 1 of 3"

printf '\n== the next task carries the ledger, defanged ==\n'
printf 'Punctuated. Use {{PLAN}} carefully.\n' > "$RELAY/build/task-1.report.md"
jq --arg r "$RELAY/build/task-1.report.md" \
   '.tasks[0].status="done" | .tasks[0].report=$r | .tasks[0].files=["src/greet.sh"] | .tasks[0].check_exit=0 | .current=2' \
   "$B" > "$B.tmp" && mv "$B.tmp" "$B"
OUT=$(run --continue --dry-run); P=$(prompt_of "$OUT")
has   "sends task 2"                     "$P" "Implement task 2 of 3"
has   "names task 2's brief"             "$P" "$RELAY/build/task-2.md"
has   "carries task 1's facts"           "$P" "Task 1 of 3: Punctuate. done, check exit 0."
has   "carries task 1's files"           "$P" "Files changed: src/greet.sh"
has   "carries task 1's note, defanged"  "$P" "Use {PLAN} carefully."
hasnt "the note's marker was not filled" "$P" "Use $PLAN carefully."
has   "hands back task 2"                "$P" "--task 2 --report <file>"
has   "does not ask Jev again"           "$OUT" "not asked again for a later task"

printf '\n== a changed plan cannot be continued ==\n'
printf '\nAn edit.\n' >> "$PLAN"
OUT=$(run --continue --dry-run); rc=$?
[ "$rc" -ne 0 ] && ok "refuses to continue a changed plan" || nope "refuses to continue a changed plan (rc $rc)"
OUT=$(run --dry-run)
has "a plain handoff of the changed plan warns it replaces the build" "$OUT" "WARNING an unfinished build of $PLAN was replaced"
has "and starts again at task 1" "$(prompt_of "$OUT")" "Implement task 1 of 3"

printf '\n== a review paused for budget is sent, not the task, and only once the budget allows ==\n'
run >/dev/null   # the changed plan starts a new build at task 1
jq '.tasks[0].status="reviewing" | .tasks[0].session="s9" | .tasks[0].usage={calls:3,input:5000000,first:1,peak:1}
    | .tasks[0].review={record_id:"r1",verdict:"review",pending:true,delivered:false,sent_at:"",first_event:"e1"}
    | .state="stopped" | .stop_reason="budget" | .stop_task=1' "$B" > "$B.tmp" && mv "$B.tmp" "$B"
rm -f "$TMP/calls" "$TMP/calls.resets"
OUT=$(run); rc=$?
[ "$rc" -ne 0 ] && ok "refuses while the task budget is still spent (rc $rc)" || nope "refuses while the task budget is still spent"
has "says it is waiting for budget" "$OUT" "waiting for budget"
[ ! -s "$TMP/calls" ] && ok "sent nothing" || nope "sent nothing"

jq '.tasks[0].usage.input=1000' "$B" > "$B.tmp" && mv "$B.tmp" "$B"
OUT=$(run --dry-run); rc=$?
has "a dry run names the review" "$OUT" "paused at its review"
has "a dry run sends nothing" "$OUT" "the stored review prompt would go to reviewer"
[ "$(jq -r '.tasks[0].review.pending' "$B")" = true ] && ok "a dry run leaves the review pending" || nope "a dry run leaves the review pending"
OUT=$(run); rc=$?
[ "$rc" -eq 0 ] && ok "sends the review (rc $rc)" || nope "sends the review (rc $rc): $OUT"
has   "the reviewer gets the review prompt" "$(cat "$TMP/calls")" "Tandem asks for a review of task 1"
[ "$(cat "$TMP/calls.target" 2>/dev/null)" = reviewer ] && ok "it goes to the reviewer pane, never the builder" || nope "it went to $(cat "$TMP/calls.target" 2>/dev/null)"
hasnt "not the task again"                "$(cat "$TMP/calls")" "Implement task"
[ ! -e "$TMP/calls.resets" ] && ok "the builder conversation was not reset" || nope "the builder conversation was not reset"
[ "$(jq -r '.tasks[0].review.delivered' "$B")" = true ] && ok "records the delivery" || nope "records the delivery"
[ "$(jq -r '.tasks[0].review.pending // false' "$B")" = false ] && ok "clears the pending flag" || nope "clears the pending flag"
[ "$(jq -r '.tasks[0].session' "$B")" = s9 ] && ok "keeps the implementation session" || nope "keeps the implementation session"
[ "$(jq -r '.tasks[0].status' "$B")" = reviewing ] && ok "task 1 is still in review" || nope "task 1 is still in review"
OUT=$(run); rc=$?
[ "$rc" -ne 0 ] && ok "a delivered review is not sent twice (rc $rc)" || nope "a delivered review is not sent twice"

printf '\n== a legacy build with no reviewer pane is refused, naming the remedy ==\n'
mv "$RELAY/reviewer.json" "$TMP/reviewer.json.kept"
rm -f "$TMP/calls"
OUT=$(run); rc=$?
[ "$rc" -eq 23 ] && ok "handoff refuses with exit 23" || nope "handoff rc $rc: $OUT"
has   "it says the builder never reviews" "$OUT" "the builder never reviews"
has   "it names the reviewer block"       "$OUT" '"reviewer" block in .tandem.json'
has   "it names tandem-pair --reviewer"   "$OUT" "tandem-pair --reviewer KIND"
hasnt "it does not blame a durable run"   "$OUT" "no durable run was started"
[ ! -e "$TMP/calls" ] && ok "and sends no prompt" || nope "a prompt was sent"
mv "$TMP/reviewer.json.kept" "$RELAY/reviewer.json"

printf '\n== a relay from an engine this install does not speak is refused ==\n'
mkdir -p "$RELAY/durable"
for ver in '"engine_version":99,"protocol_version":99' '"engine_version":2,"protocol_version":3'; do
  printf '{%s,"run_id":"%s"}\n' "$ver" "$(printf 'a%.0s' $(seq 1 32))" > "$RELAY/active-run.json"
  BEFORE=$(cd "$RELAY" && find . -type f -print0 | sort -z | xargs -0 shasum -a 256)
  rm -f "$TMP/calls"
  OUT=$(run); rc=$?
  [ "$rc" -eq 23 ] && ok "handoff refuses ($ver) with exit 23" || nope "handoff rc $rc: $OUT"
  has "and names the remedy" "$OUT" "install.sh"
  [ "$BEFORE" = "$(cd "$RELAY" && find . -type f -print0 | sort -z | xargs -0 shasum -a 256)" ] && ok "and writes nothing" || nope "handoff changed the relay"
  [ ! -e "$TMP/calls" ] && ok "and sends no prompt" || nope "a prompt was sent"
done
rm -f "$RELAY/active-run.json"

printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

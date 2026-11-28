#!/usr/bin/env bash
# A plan review goes to the reviewer pane, and only there.
#
# The pair has a Claude builder and a Codex reviewer, so the reset typed shows
# which kind was used: /new is Codex's, /clear is Claude's.
#
# Runs the real bin/tandem-review and tandem binary with the fake Herdr.
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
mkdir -p "$TMP/bin"
(cd "$ROOT_DIR" && go build -o "$TMP/bin/tandem-real" ./cmd/tandem) || { echo "could not build tandem"; exit 1; }
# Trace even dry-run requests to the baseline and reset helpers.
cat > "$TMP/bin/tandem" <<'STUB'
#!/usr/bin/env bash
case "$1 $2" in
  "gate baseline-check"|"agent reset"|"agent prompt") printf '%s\n' "$1 $2" >> "$FAKE/review-ops" ;;
esac
exec "${0%/*}/tandem-real" "$@"
STUB
chmod +x "$TMP/bin/tandem"
# A jq that fails: tandem-review asks tandem, so any jq call is a bug.
printf '#!/bin/sh\necho "jq was called: $*" >&2\nexit 99\n' > "$TMP/bin/jq"
chmod +x "$TMP/bin/jq"
. "$ROOT_DIR/tests/lib/fake-herdr.sh"
. "$ROOT_DIR/tests/lib/fake-socket.sh"
FAKE="$TMP/fake"
fake_herdr_install "$TMP/bin" "$FAKE"
FAKE="$FAKE" PATH="$TMP/bin:$PATH" fake_socket_start "$TMP" || exit 1

REPO="$TMP/repo"; MB="$REPO/.tandem-relay"
mkdir -p "$REPO/docs/plans" "$MB"; git -C "$REPO" init -q; REPO=$(cd "$REPO" && pwd -P); MB="$REPO/.tandem-relay"
printf '{"plans_dir":"docs/plans","check_command":"true"}\n' > "$REPO/.tandem.json"
PLAN="$REPO/docs/plans/p.md"
printf '# plan\n### Task 1: one\nstep\n' > "$PLAN"
write_builder "$MB" claude
REVIEWER_REC='{"kind":"codex","name":"reviewer"}'

# review [args...]: run tandem-review against a fresh fake pane; sets OUT, RC.
review() {
  rm -f "$FAKE/prompts" "$FAKE/resets" "$FAKE/review-ops" "$FAKE"/prompt.*.txt
  fake_set_state idle
  OUT=$(cd "$REPO" && env -u HERDR_DECISION PATH="$TMP/bin:$ROOT_DIR/bin:/usr/bin:/bin" \
        HERDR_ENV=1 FAKE="$FAKE" REPO="$REPO" tandem-review "$PLAN" "$@" 2>&1); RC=$?
}
sent_to()  { cut -f1 "$FAKE/prompts" 2>/dev/null | tr '\n' ' '; }
resets()   { cat "$FAKE/resets" 2>/dev/null | tr '\t\n' ': '; }
# Compare with 711ed6d after normalizing the checkout prefix and delivery ID.
check_prompt() {
  local mode="$1" expected="$2" measured
  measured=$(sed -e "s|$REPO|/repo|g" \
      -e 's/^(tandem delivery [[:xdigit:]-]*)$/(tandem delivery 00000000-0000-0000-0000-000000000000)/' \
      "$FAKE/prompt.1.txt" | cksum)
  [ "$measured" = "$expected" ] && ok "$mode prompt matches 711ed6d (cksum, bytes: $measured)" \
    || nope "$mode prompt checksum and byte count: expected $expected, got $measured"
}

# Run these same cases on each revision to measure the preflight and skill.
# Keep the fixture ahead of the candidate-only assertions so it can be copied
# unchanged to a baseline checkout.
if [ -n "${TANDEM_WORKFLOW_EVAL_ARM:-}" ]; then
  ARM="$TANDEM_WORKFLOW_EVAL_ARM"
  case "$ARM" in candidate|711ed6d) ;; *) exit 2 ;; esac
  printf '%s\n' "$REVIEWER_REC" > "$MB/reviewer.json"
  for SCENARIO in clean invalid-plan; do
    if [ "$SCENARIO" = invalid-plan ]; then
      printf '# plan\n### Task 1: one\nTask-ID: one\nTask-ID: two\nstep\n' > "$PLAN"
    fi
    review
    count=0; bytes=0; state=rejected
    if [ -s "$FAKE/prompts" ]; then
      count=$(wc -l < "$FAKE/prompts" | tr -d ' ')
      bytes=$(wc -c < "$FAKE/prompt.1.txt" | tr -d ' ')
      [ "$count" = 1 ] && [ "$(sent_to)" = "reviewer " ] && [ "$bytes" -gt 0 ] || exit 1
      state=review-dispatched
    fi
    if [ "$SCENARIO/$ARM" = invalid-plan/candidate ]; then
      [ "$RC" = 1 ] && [ "$count" = 0 ] && [ ! -s "$FAKE/review-ops" ] && [ ! -s "$FAKE/resets" ] || exit 1
      case "$OUT" in *"Task-ID is given twice"*) ;; *) printf '%s\n' "$OUT"; exit 1 ;; esac
    else
      [ "$RC" = 0 ] && [ "$count" = 1 ] || { printf '%s\n' "$OUT"; exit 1; }
    fi
    printf 'WORKFLOW_PLAN %s %s %s reviewer_dispatches=%s rendered_bytes=%s exit=%s\n' \
      "$ARM" "$SCENARIO" "$state" "$count" "$bytes" "$RC"
  done
  printf 'WORKFLOW_SKILL %s bytes=%s\n' "$ARM" "$(wc -c < "$ROOT_DIR/skills/tandem-plan/SKILL.md" | tr -d ' ')"
  exit 0
fi

printf '== a reviewer is recorded ==\n'
printf '%s\n' "$REVIEWER_REC" > "$MB/reviewer.json"; rm -f "$FAKE/absent"
review --dry-run
[ "$RC" -eq 0 ] && ok "a dry run succeeds" || nope "a dry run succeeds (rc $RC: $OUT)"
has "the header names the reviewer pane" "$(printf '%s' "$OUT" | head -1)" "tandem-review: reviewer reviewer (pane"
[ ! -s "$FAKE/prompts" ] && [ ! -s "$FAKE/resets" ] && ok "a dry run types nothing" || nope "a dry run typed: $(sent_to) $(resets)"

review
[ "$RC" -eq 0 ] && ok "a review succeeds" || nope "a review succeeds (rc $RC: $OUT)"
[ "$(sent_to)" = "reviewer " ] && ok "the prompt goes to the reviewer, and only there" || nope "prompted: $(sent_to)"
[ "$(resets)" = "reviewer:/new " ] && ok "the reviewer is reset with Codex's /new, not the builder's /clear" || nope "resets: $(resets)"
has "delivery is confirmed against the reviewer" "$OUT" "review requested (reviewer now working)"
has "the reviewer's sidebar label names the plan" "$(cat "$FAKE/metadata" 2>/dev/null)" \
  "pane report-metadata w1:reviewer --source tandem --token activity=reviewing p"
check_prompt full '590154416 3057'

printf '\n== the prompt does not assume its reader builds ==\n'
P=$(cat "$FAKE/prompt.1.txt" 2>/dev/null)
hasno "no 'before you implement it'" "$P" "before you implement it"
has "says another agent implements" "$P" "Review the plan at $PLAN. Another agent will implement it."
has "still forbids starting task 1" "$P" "Do not start"
hasno "does not tell the reader it builds" "$P" "Those are yours to decide when you build"
has "names the reviewer in the verdict command it asks for" "$P" 'tandem-verdict --plan "'"$PLAN"'" --role reviewer'

printf '\n== class 4 asks about the builder'"'"'s authority, not the reviewer'"'"'s ==\n'
has "class 4 names the builder's authority" "$P" "a step needs authority the builder"
has "with the builder's kind and autonomy from builder.json" "$P" "(claude, autonomy auto, from builder.json) does not have"
has "says the reviewer's own sandbox proves nothing about the builder" "$P" "proves nothing about the builder"
has "and asks for 'could not verify', not an objection" "$P" 'report it as "could not verify", not as an objection'
hasno "no longer asks about authority the reader does not have" "$P" "authority you do not have"

printf '\n== a small change uses the diff prompt ==\n'
ID=20260910-000001-1-1
REVIEWS="$MB/reviews"
cp "$PLAN" "$REVIEWS/$ID.plan.md"
printf 'review body\n' > "$REVIEWS/$ID.md"
printf '{"plan":"%s","verdict":"clean","kind":"claude","autonomy":"auto","reviewed_by":"reviewer","reviewer_kind":"codex"}\n' \
  "$PLAN" > "$REVIEWS/$ID.json"
printf '\nOne more step.\n' >> "$PLAN"
review
[ "$RC" -eq 0 ] && ok "a diff review succeeds" || nope "a diff review succeeds (rc $RC: $OUT)"
has "reports a diff review" "$OUT" "review: the changes since review $ID"
[ "$(sent_to)" = "reviewer " ] && ok "one diff prompt reaches the reviewer" || nope "prompted: $(sent_to)"
check_prompt diff '3567749102 3724'

printf '\n== invalid plans stop before review operations ==\n'
COUNTER="$TMP/check-runs"
printf '{"plans_dir":"docs/plans","check_command":"printf x >> %s","context_limit":1}\n' "$COUNTER" > "$REPO/.tandem.json"
rejects_plan() {
  local diag="$1" count
  shift
  review "$@"
  [ "$RC" -eq 1 ] && ok "$diag ($*): rejected" || nope "$diag ($*): rc $RC"
  count=$(printf '%s\n' "$OUT" | grep -Fc "$diag")
  [ "$count" = 1 ] && ok "the task and diagnostic appear once" || nope "expected one diagnostic: $OUT"
  if [ ! -e "$COUNTER" ] && [ ! -s "$FAKE/review-ops" ] && [ ! -s "$FAKE/resets" ] && [ ! -s "$FAKE/prompts" ]; then
    ok "no baseline, reset or delivery"
  else
    nope "unexpected review operations: $(cat "$FAKE/review-ops" 2>/dev/null)"
  fi
  rm -f "$COUNTER"
}
# The duplicate is a small edit to the reviewed snapshot and would take the
# diff path. Check the resumed path too.
printf '# plan\n### Task 1: one\nTask-ID: one\nTask-ID: two\nstep\n' > "$PLAN"
SCOPE=$("$TMP/bin/tandem-real" gate scope --plan "$PLAN" --root "$REPO")
[ "$SCOPE" = "$(printf 'diff\n%s' "$ID")" ] && ok "the duplicate fixture qualifies for a diff review" \
  || nope "the duplicate fixture scope: $SCOPE"
rejects_plan "task 1: Task-ID is given twice"
rejects_plan "task 1: Task-ID is given twice" --dry-run
rejects_plan "task 1: Task-ID is given twice" --accepted-tasks one
rejects_plan "task 1: Task-ID is given twice" --accepted-tasks one --dry-run
cp "$REVIEWS/$ID.plan.md" "$TMP/valid-plan"
rm -f "$REVIEWS/$ID.json" "$REVIEWS/$ID.md" "$REVIEWS/$ID.plan.md" "$REVIEWS/$ID.next.diff"
printf '# plan\n### Task 1: one\nTasks-ID: one\nstep\n' > "$PLAN"
rejects_plan 'task 1: "Tasks-ID" looks like a metadata field'
rejects_plan 'task 1: "Tasks-ID" looks like a metadata field' --dry-run
cp "$TMP/valid-plan" "$PLAN"

printf '\n== sizing requires resolved handoff settings ==\n'
review --dry-run
[ "$RC" -eq 0 ] && ok "review leaves the future handoff limit unresolved" || nope "review rc $RC: $OUT"
sized_gate() {
  OUT=$(TANDEM_STATE_HOME="$TMP/gate-state" "$TMP/bin/tandem" gate validate-plan \
        --plan "$PLAN" --root "$REPO" --builder-kind claude --context-limit "$1" 2>&1); RC=$?
}
sized_gate 1
[ "$RC" -eq 23 ] && ok "a known 1-byte handoff limit refuses" || nope "sizing rc $RC: $OUT"
has "the size refusal names the task" "$OUT" "task-1 needs"
sized_gate 24000
[ "$RC" -eq 0 ] && [ -z "$OUT" ] && ok "a plan within the known limit passes silently" || nope "sizing rc $RC: $OUT"
[ ! -e "$TMP/gate-state" ] && [ ! -e "$MB/active-run.json" ] \
  && ok "sizing created no run state" || nope "sizing created run state"
printf '{"plans_dir":"docs/plans","check_command":"true"}\n' > "$REPO/.tandem.json"

printf '\n== Codex asks where the new conversation should run (issue 88) ==\n'
# Codex 0.169 answers /new with a checkout picker that Herdr reports as idle.
# tandem picks its first row, the current checkout, since the reviewer reads
# the repo in place.
SCREENS="$ROOT_DIR/internal/agent/testdata/screens"
keys() { cat "$FAKE/keys" 2>/dev/null | tr '\t\n' ': '; }
cp "$SCREENS/codex-new-conversation-picker.txt" "$FAKE/picker"; rm -f "$FAKE/keys" "$FAKE/pane"
review
[ "$RC" -eq 0 ] && ok "the review is sent" || nope "the review is sent (rc $RC: $OUT)"
[ "$(keys)" = "reviewer:enter " ] && ok "Enter answers the picker once, in the reviewer's pane" || nope "keys: $(keys)"
[ "$(sent_to)" = "reviewer " ] && ok "the prompt reaches the reviewer" || nope "prompted: $(sent_to)"
[ ! -e "$FAKE/pane" ] && ok "the picker is gone" || nope "the pane still shows: $(cat "$FAKE/pane")"

# With "Create new Git worktree" highlighted, Enter would make a new checkout.
# tandem presses nothing and sends nothing.
cp "$SCREENS/codex-new-conversation-picker-moved.txt" "$FAKE/picker"; rm -f "$FAKE/keys" "$FAKE/pane"
review
[ "$RC" -ne 0 ] && ok "a picker on its second row stops the review" || nope "a picker on its second row stops the review (rc $RC)"
[ ! -s "$FAKE/keys" ] && ok "no key is pressed into it" || nope "keys: $(keys)"
[ ! -s "$FAKE/prompts" ] && ok "nothing is typed" || nope "prompted: $(sent_to)"
rm -f "$FAKE/picker" "$FAKE/keys" "$FAKE/pane"

printf '\n== no builder recorded: nothing is sent ==\n'
mv "$MB/builder.json" "$MB/builder.json.bak"
review
[ "$RC" -eq 1 ] && ok "exits 1" || nope "exits 1 (rc $RC: $OUT)"
has "says to run tandem-pair" "$OUT" "Run tandem-pair"
[ ! -s "$FAKE/prompts" ] && [ ! -s "$FAKE/resets" ] && ok "types nothing into any pane" || nope "typed: $(sent_to) $(resets)"
mv "$MB/builder.json.bak" "$MB/builder.json"

printf '\n== an incomplete builder record: nothing is sent ==\n'
cp "$MB/builder.json" "$MB/builder.json.bak"
for rec in '{"kind":"claude"}' 'not json'; do
  printf '%s\n' "$rec" > "$MB/builder.json"
  review
  [ "$RC" -eq 1 ] && ok "builder.json $rec: exits 1" || nope "builder.json $rec: exits 1 (rc $RC: $OUT)"
  has "builder.json $rec: says no complete builder identity is recorded" "$OUT" "no complete builder identity"
  [ ! -s "$FAKE/prompts" ] && [ ! -s "$FAKE/resets" ] && ok "builder.json $rec: types nothing" || nope "typed: $(sent_to) $(resets)"
done
mv "$MB/builder.json.bak" "$MB/builder.json"

printf '\n== a manifest that is not a JSON object is named ==\n'
mv "$REPO/.tandem.json" "$REPO/.tandem.json.bak"
printf '[1]\n' > "$REPO/.tandem.json"
review --dry-run
has "warns that the manifest was ignored" "$OUT" "tandem-review: WARNING $REPO/.tandem.json is not a JSON object, so its settings were ignored."
mv "$REPO/.tandem.json.bak" "$REPO/.tandem.json"

printf '\n== a missing prompt template: nothing is sent ==\n'
cp -R "$ROOT_DIR/bin" "$TMP/rbin"
rm "$TMP/rbin/lib/review-rules.txt"
rm -f "$FAKE/prompts" "$FAKE/resets"; fake_set_state idle
OUT=$(cd "$REPO" && PATH="$TMP/bin:$ROOT_DIR/bin:/usr/bin:/bin" HERDR_ENV=1 FAKE="$FAKE" REPO="$REPO" \
      "$TMP/rbin/tandem-review" "$PLAN" 2>&1); RC=$?
[ "$RC" -eq 1 ] && ok "exits 1" || nope "exits 1 (rc $RC: $OUT)"
has "names the missing template" "$OUT" "the prompt template $TMP/rbin/lib/review-rules.txt is missing or empty; re-run install.sh. NOTHING WAS SENT."
[ ! -s "$FAKE/prompts" ] && [ ! -s "$FAKE/resets" ] && ok "types nothing into any pane" || nope "typed: $(sent_to) $(resets)"

printf '\n== --builder is gone: the builder never reviews ==\n'
review --builder
[ "$RC" -eq 2 ] && ok "exits 2" || nope "exits 2 (rc $RC: $OUT)"
has "says the builder never reviews" "$OUT" "the builder never reviews"
[ ! -s "$FAKE/prompts" ] && [ ! -s "$FAKE/resets" ] && ok "types nothing into any pane" || nope "typed: $(sent_to) $(resets)"

printf '\n== the recorded reviewer has no pane ==\n'
printf 'reviewer\n' > "$FAKE/absent"
review
[ "$RC" -eq 1 ] && ok "exits 1" || nope "exits 1 (rc $RC)"
has "says to re-pair" "$OUT" "Re-run tandem-pair"
hasno "does not offer the builder as a way out" "$OUT" "--builder"
has "says nothing was sent" "$OUT" "NOTHING WAS SENT"
[ ! -s "$FAKE/prompts" ] && [ ! -s "$FAKE/resets" ] && ok "types nothing into any pane" || nope "typed: $(sent_to) $(resets)"
rm -f "$FAKE/absent"

printf '\n== reviewer.json cannot be read ==\n'
printf 'not json\n' > "$MB/reviewer.json"
review
[ "$RC" -eq 1 ] && ok "exits 1" || nope "exits 1 (rc $RC)"
has "names the file" "$OUT" "$MB/reviewer.json"
[ ! -s "$FAKE/prompts" ] && [ ! -s "$FAKE/resets" ] && ok "types nothing into any pane" || nope "typed: $(sent_to) $(resets)"

printf '\n== no reviewer recorded: nothing is sent, and the builder is not a fallback ==\n'
rm -f "$MB/reviewer.json"
for mode in "" --dry-run; do
  review $mode
  [ "$RC" -eq 1 ] && ok "${mode:-a review} exits 1" || nope "${mode:-a review} exits 1 (rc $RC: $OUT)"
  has "${mode:-a review} says the pair has no reviewer" "$OUT" "this pair has no reviewer"
  has "${mode:-a review} names the block remedy" "$OUT" "Declare a \"reviewer\" block in .tandem.json and re-run tandem-pair"
  has "${mode:-a review} names the flag remedy" "$OUT" "tandem-pair --reviewer KIND"
  [ ! -s "$FAKE/prompts" ] && [ ! -s "$FAKE/resets" ] && ok "${mode:-a review} types nothing into any pane" || nope "typed: $(sent_to) $(resets)"
done

printf '\n%s passed, %s failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

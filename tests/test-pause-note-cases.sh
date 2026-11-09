#!/usr/bin/env bash
# Each pause note reaches the planner pane through the real bridge.
#
# No model is called. The shipped tandem binary, a real bin/tandem-bridge and a
# fake Herdr run one pass at a time (--once). The planner's pane is pinned idle
# and the checks are what it was typed, to whom, and how many times:
#   - a provider that rejects every turn pauses dispatch_failed;
#   - a send nobody saw start waits for an acknowledgement before any note is typed;
#   - a startup dialog leaves the send delivery_unknown, and the note names the builder;
#   - a failed check names the task and offers no waiver that cannot move the run;
#   - a plan edited mid-run, and a deliberate stop, each get their own note;
#   - a reviewer's changes_requested starts a repair and sends no note.
# The quiet pauses, which need a bridge that stays up, are in
# tests/test-pause-note-quiet.sh.
set -uo pipefail
. "$(cd "$(dirname "$0")" && pwd)/lib/note-cases.sh"

printf '== a provider that rejects every turn: dispatch_failed ==\n'
new_case provider true
printf 'There is an issue with the selected model.\n' > "$FAKE/pane"
# Each prompt's turn ends 0.3 s after it is typed, as a rejected model does.
( exec 2>/dev/null; n=0; while :; do c=$(wc -l < "$FAKE/prompts" 2>/dev/null || echo 0)
    [ "$c" -le "$n" ] || { n=$c; sleep 0.3; fake_set_state idle; }; sleep 0.05; done ) &
DRIVER=$!
# Each retry resets into a new conversation, so two logs hold the prompt's nonce.
# SessionContaining refuses two matches, the transport drops that error, and
# usedTokens then reads the empty session as "might have worked". Deleting the
# logs leaves one match per attempt.
for _ in 1 2 3 4 5 6 7 8; do [ "$(state)" = paused/dispatch_failed ] && break; rm -rf "$CODEX_HOME"; pass; sleep 1; done
stop_driver
[ "$(state)" = paused/dispatch_failed ] && ok "the run pauses dispatch_failed after its retries" || nope "state $(state)"
NOTE=$(last_note)
[ "$(sent_to planner)" = 1 ] && ok "the planner got one note" || nope "notes: $(sent_to planner)"
has "it says the task never reached a working builder" "$NOTE" "stopped: the task never reached a working builder."
has "it shows what the pane said" "$NOTE" "There is an issue with the selected model."
has "it names the builder pane" "$NOTE" "Builder pane: builder"
once_only provider

printf '\n== a sent prompt nobody acknowledges: writer_unknown once the window passes ==\n'
# The window is 10 minutes (state.AckWindow); TANDEM_ACK_WINDOW_MS shortens it.
for pane in working idle; do
  ACK_MS=1500; new_case "ack-$pane" true
  pass
  [ "$(sent_to builder)" = 1 ] && ok "$pane: the task is typed once" || nope "$pane: builder prompts: $(sent_to builder)"
  [ "$pane" = idle ] && fake_set_state idle
  [ "$(sent_to planner)" = 0 ] && ok "$pane: nothing is said inside the window" || nope "$pane: early note"
  sleep 2; pass 2
  [ "$(state)" = paused/writer_unknown ] && ok "$pane: the run pauses writer_unknown after the window" || nope "$pane: state $(state)"
  [ "$(sent_to planner)" = 1 ] && ok "$pane: the planner got one note" || nope "$pane: notes: $(sent_to planner)"
  has "$pane: it says tandem cannot tell if the builder got the task" "$(last_note)" "tandem cannot tell whether the builder received the task."
  has "$pane: it names the builder pane" "$(last_note)" "Builder pane: builder"
  once_only "ack-$pane"
  ACK_MS=""
done

printf '\n== a send nobody saw start: no note while an acknowledgement can arrive ==\n'
new_case waiting true
: > "$FAKE/swallow"
pass
[ "$(state)" = running/ ] && ok "the run keeps running" || nope "state $(state)"
[ "$(sent_to builder)" = 1 ] && ok "the task was typed once" || nope "builder prompts: $(sent_to builder)"
[ "$(sent_to planner)" = 0 ] && ok "the planner got no note" || nope "notes: $(sent_to planner)"
rm -f "$FAKE/swallow"

printf '\n== a startup dialog: the send becomes delivery_unknown ==\n'
new_case dialog true
# A pane at a native dialog reports idle and swallows the prompt.
: > "$FAKE/swallow"
# The grace is shortened to 1 ms, so the second pass finds it over.
GRACE_MS=1
pass 2
GRACE_MS=""
[ "$(state)" = paused/writer_unknown ] && ok "the run pauses writer_unknown" || nope "state $(state)"
has "the send is delivery_unknown" "$(td state export --run "$(td build status --root "$R" --json | jq -r .run_id)" --output "$TMP/exp-dialog" --root "$R" | jq -r '.unresolved[]')" "delivery_unknown"
[ "$(sent_to builder)" = 1 ] && ok "the task was typed once" || nope "builder prompts: $(sent_to builder)"
NOTE=$(last_note)
[ "$(sent_to planner)" = 1 ] && ok "the planner got one note" || nope "notes: $(sent_to planner)"
has "it says tandem cannot tell if the builder got the task" "$NOTE" "tandem cannot tell whether the builder received the task."
has "it names the builder pane" "$NOTE" "Builder pane: builder"
has "and tells the planner to look at that pane" "$NOTE" "Look at the builder pane builder for the prompt for"
once_only dialog
rm -f "$FAKE/swallow"

printf '\n== a failed check names the task ==\n'
new_case check false
builder_turn
[ "$(state)" = paused/check_failed ] && ok "the run pauses check_failed" || nope "state $(state)"
NOTE=$(last_note)
[ "$(sent_to planner)" = 1 ] && ok "the planner got one note" || nope "notes: $(sent_to planner)"
has "it says the check failed" "$NOTE" "stopped: the check for the task failed."
has "it names the task" "$NOTE" "Task: task-1"
# A waiver does not move a paused run (pause-note.sh), so the note must not offer one.
lack "it offers no waiver" "$NOTE" "--waive"
has "it ends in the restart route" "$NOTE" "tandem state archive --run"
once_only check

printf '\n== a plan edited mid-run: plan_changed ==\n'
new_case plan true
pass 2
printf '\nA line added after the run started.\n' >> "$R/plan.md"
pass 2
[ "$(state)" = paused/plan_changed ] && ok "the run pauses plan_changed" || nope "state $(state)"
NOTE=$(last_note)
[ "$(sent_to planner)" = 1 ] && ok "the planner got one note" || nope "notes: $(sent_to planner)"
has "it says the plan changed" "$NOTE" "stopped: the plan file changed after the run started."
has "it says to put the plan back and continue" "$NOTE" "tandem build recover --root"
once_only plan

printf '\n== a deliberate stop: stopped ==\n'
new_case stop true
pass 2
td build stop --root "$R" >/dev/null 2>&1
pass 2
fake_set_state idle
pass 3
[ "$(state)" = paused/stopped ] && ok "the run pauses stopped" || nope "state $(state)"
NOTE=$(last_note)
[ "$(sent_to planner)" = 1 ] && ok "the planner got one note" || nope "notes: $(sent_to planner)"
has "it reports stopped" "$NOTE" "stopped: someone asked it to stop."
has "it offers tandem-unpair to close the pair" "$NOTE" "tandem-unpair"
once_only stop

printf '\n== changes_requested starts a repair and sends no note ==\n'
new_case repair true
builder_turn
RV=$(grep "^reviewer$TAB" "$FAKE/prompts" | head -1 | cut -f2-)
rf() { printf '%s' "$RV" | grep -o "\"$1\":\"[0-9a-f]*\"" | head -1 | cut -d'"' -f4; }
printf '{"schema_version":1,"job_id":"%s","attempt_id":"%s","patch_hash":"%s","tree_hash":"%s","status":"changes_requested","findings":[{"severity":"high","path":"file1.txt","disposition":"open","message":"fix it","start_line":null,"end_line":null}]}\n' \
  "$(rf job_id)" "$(rf attempt_id)" "$(rf patch_hash)" "$(rf tree_hash)" > "$TMP/review.json"
fake_set_state idle
td build review-record --root "$R" --result "$TMP/review.json" >/dev/null 2>"$TMP/err" && ok "the reviewer's changes_requested is recorded" || nope "review-record: $(cat "$TMP/err")"
pass 4
[ "$(td build status --root "$R" --json | jq '[.tasks[].attempts[]?] | length')" = 2 ] && ok "a repair attempt starts" || nope "attempts: $(td build status --root "$R" --json | jq -c '.tasks')"
[ "$(state)" = running/ ] && ok "and the run keeps running" || nope "state $(state)"
[ "$(sent_to planner)" = 0 ] && ok "the planner was told nothing" || nope "notes: $(sent_to planner)"
td build stop --root "$R" >/dev/null 2>&1
pass 2
fake_set_state idle
pass 3
[ "$(sent_to planner)" = 1 ] && has "the first note is the stop that follows" "$(last_note)" "stopped: someone asked it to stop." || nope "notes after the stop: $(sent_to planner) state $(state)"

printf '\ntests/test-pause-note-cases: passed %d, failed %d\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

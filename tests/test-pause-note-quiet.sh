#!/usr/bin/env bash
# The quiet pauses reach the planner pane through the real bridge. Each case
# runs one bridge that stays up, as a real pair has, because a bridge recovers
# when it starts and that restarts the silence clock. The window is 10 minutes
# (controller.SilenceWindow); TANDEM_SILENCE_WINDOW_MS shortens it to 3 seconds.
#   - a builder that starts and goes idle gets one note, and a late handback goes on;
#   - a handback the tree keeps from applying still pauses with one note;
#   - a builder held at a dialog gets one note, and a recover after the dialog goes on.
set -uo pipefail
. "$(cd "$(dirname "$0")" && pwd)/lib/note-cases.sh"

printf '== a builder that starts and goes idle: builder_silent ==\n'
SILENCE_MS=3000; new_case silent true
start_turn
fake_set_state idle
bridge_up
sleep 1
[ "$(sent_to planner)" = 0 ] && ok "nothing is said inside the window" || nope "early note"
for _ in $(seq 1 60); do [ "$(sent_to planner)" = 0 ] || break; sleep 0.5; done
[ "$(state)" = paused/builder_silent ] && ok "the run pauses builder_silent after the window" || nope "state $(state)"
[ "$(sent_to builder)" = 1 ] && ok "nothing more is typed to the builder" || nope "builder prompts: $(sent_to builder)"
NOTE=$(last_note)
has "it says what happened" "$NOTE" "stopped: the builder started this task, then went idle without handing anything back."
has "it gives the window the run used" "$NOTE" "The builder's pane stayed idle in the same turn for 3 seconds."
has "it gives the causes" "$NOTE" "A provider failure, an expired login, used-up quota or a turn that ended early can do this."
has "it names the builder pane" "$NOTE" "Builder pane: builder"
has "and tells the planner to look at that pane" "$NOTE" "Look at the builder pane builder."
has "it says a handback continues the run" "$NOTE" "A handback continues the run by itself."
has "it gives the way to carry on" "$NOTE" "tandem build recover --root"
has "it says a stuck handback's reason is in the bridge log" "$NOTE" "the bridge log says why"
has "and prints the real log path" "$NOTE" "grep -F 'busy:' $R/.tandem-relay/bridge.log"
has "it gives the restart route" "$NOTE" "tandem state archive --run"
has "with the git status step" "$NOTE" "check git status before you hand off"
has "that starts again at task 1" "$NOTE" "The run starts again at task 1."
sleep 3
[ "$(sent_to planner)" = 1 ] && ok "the planner got one note, never repeated" || nope "notes: $(sent_to planner)"
# The fake shares one status across panes, so typing the note moved the builder too.
fake_set_state idle
printf 'work\n' > "$R/file1.txt"; printf 'done\n' > "$TMP/report.md"
td build event new --root "$R" --kind builder_submitted "${IDENT[@]}" --payload "$TMP/report.md" >/dev/null 2>&1
for _ in $(seq 1 40); do [ "$(sent_to reviewer)" = 0 ] || break; sleep 0.5; done
[ "$(state)" = running/ ] && ok "a late handback continues the run" || nope "state $(state)"
[ "$(sent_to reviewer)" = 1 ] && ok "and the task goes on to its review" || nope "reviewer prompts: $(sent_to reviewer)"
[ "$(sent_to planner)" = 1 ] && ok "and says nothing more to the planner" || nope "notes: $(sent_to planner)"
stop_driver
SILENCE_MS=""

printf '\n== a handback the tree keeps from applying: builder_silent ==\n'
# An untracked nested repository cannot be captured exactly, so every tick fails
# as busy. The run still pauses, and the planner hears of it once.
SILENCE_MS=3000; new_case stuck true
start_turn
fake_set_state idle
mkdir "$R/nested" && git -C "$R/nested" init -q
printf 'work\n' > "$R/file1.txt"; printf 'done\n' > "$TMP/report.md"
td build event new --root "$R" --kind builder_submitted "${IDENT[@]}" --payload "$TMP/report.md" >/dev/null 2>&1
bridge_up
for _ in $(seq 1 60); do [ "$(sent_to planner)" = 0 ] || break; sleep 0.5; done
[ "$(state)" = paused/builder_silent ] && ok "the run pauses builder_silent while every tick fails" || nope "state $(state)"
NOTE=$(last_note)
has "it names the builder pane" "$NOTE" "Builder pane: builder"
has "it points to the bridge log" "$NOTE" "grep -F 'cannot be captured exactly' $R/.tandem-relay/bridge.log"
grep -qs 'cannot be captured exactly' "$TMP/bridge.log" "$R/.tandem-relay/bridge.log" && ok "and the log says why" || nope "no reason in the bridge log"
sleep 3
[ "$(sent_to planner)" = 1 ] && ok "the planner got one note, never repeated" || nope "notes: $(sent_to planner)"
fake_set_state idle
rm -rf "$R/nested"
for _ in $(seq 1 40); do [ "$(sent_to reviewer)" = 0 ] || break; sleep 0.5; done
[ "$(state)" = running/ ] && ok "a fixed tree lets the handback apply and the run continue" || nope "state $(state)"
[ "$(sent_to reviewer)" = 1 ] && ok "and the task goes on to its review" || nope "reviewer prompts: $(sent_to reviewer)"
stop_driver
SILENCE_MS=""

printf '\n== a builder waiting on a dialog: builder_blocked ==\n'
# Herdr reports a pane at an approval or question dialog as blocked. The fake
# keeps a blocked pane blocked, whatever is typed elsewhere.
SILENCE_MS=3000; new_case blocked true
start_turn
fake_set_state blocked
RUN=$(td build status --root "$R" --json | jq -r .run_id)
bridge_up
sleep 1
[ "$(sent_to planner)" = 0 ] && ok "nothing is said inside the window" || nope "early note"
for _ in $(seq 1 60); do [ "$(sent_to planner)" = 0 ] || break; sleep 0.5; done
[ "$(state)" = paused/builder_blocked ] && ok "the run pauses builder_blocked after the window" || nope "state $(state)"
[ "$(sent_to builder)" = 1 ] && ok "nothing is typed into the builder pane" || nope "builder prompts: $(sent_to builder)"
NOTE=$(last_note)
has "it says the builder waits on a dialog" "$NOTE" "stopped: the builder is waiting on an approval or question dialog in its pane."
has "it gives the window the run used" "$NOTE" "The builder's pane stayed blocked at a dialog for 3 seconds."
has "it names the builder pane" "$NOTE" "Builder pane: builder"
has "it says to answer the dialog in the pane" "$NOTE" "Answer the dialog in that pane yourself."
has "it gives the recover that continues the run" "$NOTE" "tandem state recover --run $RUN --root $R"
sleep 3
[ "$(sent_to planner)" = 1 ] && ok "the planner got one note, never repeated" || nope "notes: $(sent_to planner)"
td state recover --run "$RUN" --root "$R" >/dev/null 2>"$TMP/err"
[ "$(state)" = paused/builder_blocked ] && ok "a recover while the dialog is up keeps the pause" || nope "state $(state): $(cat "$TMP/err")"
# The owner answers the dialog and the builder works again.
fake_set_state working
td state recover --run "$RUN" --root "$R" >/dev/null 2>"$TMP/err"
[ "$(state)" = running/ ] && ok "the note's recover continues the run once the dialog is answered" || nope "state $(state): $(cat "$TMP/err")"
stop_driver
SILENCE_MS=""

printf '\ntests/test-pause-note-quiet: passed %d, failed %d\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

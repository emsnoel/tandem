#!/usr/bin/env bash
# The note a person reads when a durable run pauses (bin/lib/pause-note.sh).
# One note for every pause code tandem lists, and tandem-pause-note reading a
# real run's status. No Herdr: the run is seeded in a temp state home.
set -uo pipefail
ROOT_DIR=$(cd "$(dirname "$0")/.." && pwd)

PASS=0; FAIL=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }
has()  { case "$2" in *"$3"*) ok "$1" ;; *) nope "$1 (missing: $3): $2" ;; esac; }
lack() { case "$2" in *"$3"*) nope "$1 (found: $3): $2" ;; *) ok "$1" ;; esac; }

TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
go -C "$ROOT_DIR" build -o "$TMP/tandem-real" ./cmd/tandem || { echo "could not build tandem" >&2; exit 2; }
CODES="$TMP/codes"
"$TMP/tandem-real" build pause-codes > "$CODES"

# shellcheck source=../bin/lib/reviewer.sh
. "$ROOT_DIR/bin/lib/reviewer.sh"
# shellcheck source=../bin/lib/pause-note.sh
. "$ROOT_DIR/bin/lib/pause-note.sh"

RUN=0123456789abcdef0123456789abcdef
SPACED="/tmp/a b/repo"

printf '== every pause code has a note ==\n'
[ -s "$CODES" ] && ok "tandem lists pause codes" || nope "no pause codes"
while IFS= read -r -d '' code; do
  out=$(pause_note "$code" "$RUN" "$SPACED" task-2 builder reviewer "")
  first=$(printf '%s\n' "$out" | sed -n 1p)
  case "$first" in "[build] Run $RUN stopped: "*|"[build] Run $RUN is stopping: "*) ok "$code: first line names the run" ;; *) nope "$code: first line: $first" ;; esac
  case "$out" in *$'\nWhat to do:\n'*) ok "$code: has What to do:" ;; *) nope "$code: no What to do:" ;; esac
  printf '%s\n' "$out" | grep -q '^1\. ' && ok "$code: has a numbered step" || nope "$code: no numbered step"
  case "$out" in *"—"*) nope "$code: has an em dash" ;; *) ok "$code: no em dash" ;; esac
done < "$CODES"

printf '\n== commands keep a root with spaces as one argument ==\n'
ARGS="$TMP/args"
tandem() { printf '%s\n' "$@" >> "$ARGS"; printf -- '--end\n' >> "$ARGS"; }
n=0; bad=0
while IFS= read -r -d '' code; do
  : > "$ARGS"
  pause_note "$code" "$RUN" "$SPACED" task-2 builder reviewer "" | grep '^    tandem ' | while IFS= read -r line; do eval "${line#    }"; done
  # Each command with a --root has exactly the root as the word after it.
  if grep -qx -- '--root' "$ARGS"; then
    n=$((n+1))
    awk -v want="$SPACED" '$0=="--root"{getline; if ($0!=want) exit 1}' "$ARGS" || { nope "$code: root split"; bad=1; }
  fi
done < "$CODES"
unset -f tandem
[ "$n" -gt 10 ] && [ "$bad" -eq 0 ] && ok "$n codes print tandem commands and every root is one argument" || nope "checked $n codes, bad=$bad"
OUT=$(pause_note check_failed "$RUN" "$SPACED" task-2 b r "")
has "the root is escaped for the shell" "$OUT" 'tandem build stop --root /tmp/a\ b/repo'

printf '\n== unknown and empty codes ==\n'
for c in made_up_code ""; do
  OUT=$(pause_note "$c" "$RUN" /r "" "" "" "")
  has "'$c': says the run stopped" "$OUT" "Run $RUN stopped"
  has "'$c': points to tandem build status" "$OUT" "tandem build status --root /r"
done

printf '\n== what each note carries ==\n'
OUT=$(pause_note check_failed "$RUN" /r task-7 b r "")
has "check_failed names the task" "$OUT" "task-7"
lack "check_failed does not offer a waive that leaves the run paused" "$OUT" "--waive"
has "check_failed shows the log facts it was given" "$(pause_note check_failed "$RUN" /r task-7 b r "The check exited 1. The end of its log:
FAIL TestX")" "FAIL TestX"
OUT=$(pause_note review_unknown "$RUN" /r task-7 b rev "No valid review in the pane.")
has "review_unknown includes the reason facts" "$OUT" "No valid review in the pane."
has "review_unknown reads the reviewer pane" "$OUT" "herdr agent read rev"
has "review_unknown records the result" "$OUT" "tandem build review-record --result FILE --root /r"
OUT=$(pause_note writer_unknown "$RUN" /r task-7 bldr rev "")
has "writer_unknown names the builder pane" "$OUT" "bldr"
has "writer_unknown settles the delivery" "$OUT" "tandem state settle-delivery --run $RUN --effect EFFECT --not-read --because WHY --root /r"
OUT=$(pause_note agent_exited "$RUN" /r task-7 bldr rev "")
has "agent_exited names tandem-pair and recover" "$OUT" "tandem-pair"
has "agent_exited continues with recover" "$OUT" "tandem build recover --root /r"
OUT=$(pause_note no_reviewer "$RUN" /r "" bldr "" "")
has "no_reviewer gives the reviewer remedy" "$OUT" "$REVIEWER_REMEDY"
OUT=$(pause_note agent_unsupported "$RUN" /r "" bldr "" "")
for s in "build stop" "build wait" "state recover --run $RUN" "state archive --run $RUN"; do has "agent_unsupported lists $s" "$OUT" "tandem $s"; done
OUT=$(pause_note dispatch_failed "$RUN" /r "" bldr "" "pane says: bad model")
has "dispatch_failed keeps the reason" "$OUT" "pane says: bad model"
has "dispatch_failed restarts" "$OUT" "tandem state archive --run $RUN --root /r"
OUT=$(pause_note context_too_large "$RUN" /r task-1 b r "")
has "context_too_large offers a larger limit" "$OUT" "--context-limit N"
OUT=$(restart_steps "$RUN" /r)
has "restart_steps hands the plan off again" "$OUT" "tandem-handoff"

printf '\n== the rulings ==\n'
for c in check_failed review_failed stuck repairs_exhausted source_mutated recovery_blocked; do
  OUT=$(pause_note "$c" "$RUN" /r task-7 b r "")
  lack "$c does not offer waive" "$OUT" "--waive"
  lack "$c has no SCOPE placeholder" "$OUT" "SCOPE"
done
OUT=$(pause_note review_unknown "$RUN" /r task-7 b r "")
has "review_unknown keeps the waive, which clears it" "$OUT" "tandem build accept --task task-7 --waive review --because WHY --root /r"
has "review_unknown says a mailbox review waits 5 minutes" "$OUT" "waits 5"
OUT=$(pause_note check_failed "$RUN" /r task-7 b r "" "/plans/my plan.md" "" /m)
has "the hand-off names the real plan, quoted" "$OUT" 'tandem-handoff /plans/my\ plan.md'
OUT=$(pause_note check_failed "$RUN" /r task-7 b r "" "" "" /m)
case "$OUT" in *$'\n    tandem-handoff') ok "a run with no plan path hands off with tandem-handoff alone" ;; *) nope "no-plan hand-off: $OUT" ;; esac
OUT=$(pause_note context_too_large "$RUN" /r task-7 b r "" /p.md "" /m)
has "context_too_large puts the limit before the plan" "$OUT" "tandem-handoff --context-limit N /p.md"
OUT=$(pause_note scheduling_blocked "$RUN" /r task-7 b r "" /p.md COLL1 /m)
has "scheduling_blocked names the collaboration" "$OUT" "tandem collaboration resume --collaboration COLL1 "
OUT=$(pause_note scheduling_blocked "$RUN" /r task-7 b r "" /p.md "" /m)
lack "and offers no resume when there is none" "$OUT" "collaboration resume"
OUT=$(pause_note stopping "$RUN" /r task-7 b r "")
lack "stopping is not called stopped" "$OUT" "stopped:"
has "stopping says it waits for the current turn" "$OUT" "waiting for the builder's current turn"
OUT=$(restart_steps "$RUN" /r)
stop_at=$(printf '%s\n' "$OUT" | grep -n 'tandem build stop' | cut -d: -f1)
wait_at=$(printf '%s\n' "$OUT" | grep -n 'tandem build wait --root /r --until paused --tick' | cut -d: -f1)
rec_at=$(printf '%s\n' "$OUT" | grep -n 'tandem state recover' | cut -d: -f1)
[ -n "$stop_at" ] && [ -n "$wait_at" ] && [ "$stop_at" -lt "$wait_at" ] && [ "$wait_at" -lt "$rec_at" ] \
  && ok "restart waits for the stop to settle before it ends the open jobs" || nope "restart order: $OUT"
OUT=$(pause_note no_reviewer "$RUN" /r "" b "" "")
heading=$(printf '%s\n' "$OUT" | grep -n '^What to do:' | cut -d: -f1)
prose=$(printf '%s\n' "$OUT" | grep -n 'started before every run needed' | cut -d: -f1)
[ -n "$prose" ] && [ "$prose" -lt "$heading" ] && ok "no_reviewer prose comes above the heading" || nope "prose line $prose, heading $heading"
OUT=$(pause_note dispatch_failed "$RUN" /r "" b r "")
has "dispatch_failed gives the reviewer remedy" "$OUT" "If the pair has no reviewer: $REVIEWER_REMEDY"
OUT=$(pause_note writer_unknown "$RUN" /r task-7 bldr rev "")
has "writer_unknown: a working builder is cleared by recover" "$OUT" "tandem state recover --run $RUN --root /r"
has "writer_unknown: the export goes to a new folder" "$OUT" "--output /tmp/tandem-export-$RUN"
has "writer_unknown: pick the builder's effect" "$OUT" "builder's task prompt"
lack "writer_unknown no longer says wait and stop" "$OUT" "wait and stop here"
OUT=$(pause_note memory_conflict "$RUN" /r task-7 b r "" /p.md "" "/my relay")
has "memory_conflict greps the real bridge log for the error" "$OUT" "grep -F 'was not started' /my\\ relay/bridge.log | tail -n 5"
OUT=$(pause_note recovery_blocked "$RUN" /r task-7 b r "Why recovery stopped: nope")
has "recovery_blocked shows the stored cause" "$OUT" "Why recovery stopped: nope"
has "recovery_blocked says what it means" "$OUT" "repair allowance is used up"
OUT=$(pause_note source_mutated "$RUN" /r task-7 b r "")
lack "source_mutated does not say outside the run" "$OUT" "outside"
lack "source_mutated does not point at a check log" "$OUT" "log"
has "source_mutated says what is true of every cause" "$OUT" "changed after tandem measured them"
has "source_mutated names a person, a tool or a check" "$OUT" "A person, another tool or a check that edits files"
has "source_mutated says to set stray changes aside first" "$OUT" "Commit any stray changes or set them aside"
for c in check_failed review_failed repairs_exhausted recovery_blocked verification_identity verification_stale; do
  OUT=$(pause_note "$c" "$RUN" /r task-7 b r "")
  lack "$c with no stored fact never says above" "$OUT" "above"
  OUT=$(pause_note "$c" "$RUN" /r task-7 b r "A stored fact.")
  case "$c" in verification_*|recovery_blocked|check_failed|review_failed|repairs_exhausted) has "$c with a fact points at it" "$OUT" "above" ;; esac
done
OUT=$(pause_note agent_unsupported "$RUN" /r "" bldr "" "")
lack "agent_unsupported uses the shared wait, not a bare tick" "$OUT" "tandem build tick"
has "agent_unsupported waits until paused" "$OUT" "tandem build wait --root /r --until paused --tick"

printf '\n== tandem-pause-note reads the run status ==\n'
# A stand-in for tandem that answers the three questions the command asks.
# STATE_FILE holds the status, as nine NUL-separated fields.
mkdir -p "$TMP/bin"
cat > "$TMP/bin/tandem" <<STUB
#!/usr/bin/env bash
case "\$1 \$2" in
  "paths relay") printf '%s\\n' "\$4/.tandem-relay" ;;
  "state active-run") [ -n "\${NO_RUN:-}" ] && exit 1; printf '%s\\n' $RUN ;;
  "build status") cat "\$STATE_FILE" ;;
  *) exit 2 ;;
esac
STUB
chmod +x "$TMP/bin/tandem"
ln -s "$ROOT_DIR/bin/tandem-pause-note" "$TMP/bin/tandem-pause-note"
PN() { env PATH="$TMP/bin:$PATH" "$@"; }
fields() { printf '%s\0%s\0%s\0%s\0%s\0%s\0%s\0%s\0%s\0' "$@" > "$TMP/status"; }

fields paused stuck sent "It said stuck." task-3 bldr rev "/p/my plan.md" COLL9
OUT=$(PN STATE_FILE="$TMP/status" tandem-pause-note --root "$SPACED" 2>&1); RC=$?
[ "$RC" -eq 0 ] && ok "a paused run prints its note and exits 0" || nope "rc $RC: $OUT"
has "the note names the run" "$OUT" "[build] Run $RUN stopped: the builder said it could not finish the task."
has "it carries the task" "$OUT" "Task: task-3"
has "it carries the reason" "$OUT" "It said stuck."
has "and quotes the root" "$OUT" 'tandem build stop --root /tmp/a\ b/repo'
has "it hands off the run's real plan" "$OUT" 'tandem-handoff /p/my\ plan.md'
# Status drops a fact it cannot read (a missing or damaged log), leaving the reason empty.
fields paused check_failed sent "" task-3 bldr rev "/p/plan.md" ""
OUT=$(PN STATE_FILE="$TMP/status" tandem-pause-note --root /r 2>&1); RC=$?
[ "$RC" -eq 0 ] && has "a paused run whose log was unreadable still gets a note" "$OUT" "What to do:" || nope "unreadable log: rc $RC: $OUT"
lack "and the note does not point at a log above" "$OUT" "above"
has "and says where else to look" "$OUT" "Run the task's check command yourself"
fields running "" sent "" task-3 bldr rev "" ""
OUT=$(PN STATE_FILE="$TMP/status" tandem-pause-note --root /r 2>&1); RC=$?
[ "$RC" -eq 0 ] && has "a running run is not paused" "$OUT" "is running, not paused" || nope "running: rc $RC: $OUT"
OUT=$(PN NO_RUN=1 tandem-pause-note --root /r 2>&1); RC=$?
[ "$RC" -eq 0 ] && has "no run says so" "$OUT" "no durable run" || nope "no run: rc $RC: $OUT"
OUT=$(PN tandem-pause-note 2>&1); RC=$?
[ "$RC" -ne 0 ] && ok "a missing --root is refused" || nope "missing --root accepted: $OUT"

printf '\ntests/test-pause-notes: passed %d, failed %d\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

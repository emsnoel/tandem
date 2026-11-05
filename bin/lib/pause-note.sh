# shellcheck shell=bash
# bin/lib/pause-note.sh: the note a person reads when a durable run pauses.
#
# Sourced by tandem-bridge, tandem-handoff and tandem-pause-note, never run.
# Needs REVIEWER_REMEDY from lib/reviewer.sh. The Go side reports facts (the
# code and what the run stored about it) and this file says what to do.
#
# Every `tandem` command in a note sits on its own line, indented four spaces,
# with the root quoted by printf %q so a path with spaces stays one argument.
#
# Waiving a check, a review or an implementation does not move a paused run
# except for review_unknown, so only that note offers it.

_pn_n=0
# The plan path and the relay folder of the note being written.
_pn_plan=""
_pn_mailbox=""

# _pn_step TEXT [COMMAND]: one numbered step, then its command if it has one.
_pn_step() {
  _pn_n=$(( _pn_n + 1 ))
  printf '%d. %s\n' "$_pn_n" "$1"
  [[ -z "${2:-}" ]] || printf '    %s\n' "$2"
}

# _pn_teardown RUN ROOT: stop the run, wait for it to settle, end its open
# jobs, archive it. The wait ticks the run, which is what finishes a stop.
_pn_teardown() {
  local q; q=$(printf %q "$2")
  _pn_step "Stop the run." "tandem build stop --root $q"
  _pn_step "Wait until the run is paused. A stop waits for the builder's current turn and the wait ticks the run, so it can take a few minutes. If it times out, run it again." \
    "tandem build wait --root $q --until paused --tick --timeout 120000"
  _pn_step "End its open jobs. An open job blocks the archive." "tandem state recover --run $1 --root $q"
  _pn_step "Archive the run." "tandem state archive --run $1 --root $q"
}

# _pn_handoff FLAGS: hand the plan off again, which starts at task 1. A run
# with no stored plan path prints tandem-handoff alone, which picks a plan.
_pn_handoff() {
  local cmd="tandem-handoff"
  [[ -z "${1:-}" ]] || cmd="$cmd $1"
  [[ -z "$_pn_plan" ]] || cmd="$cmd $(printf %q "$_pn_plan")"
  _pn_step "Hand the plan off again. The run starts again at task 1." "$cmd"
}

# restart_steps RUN ROOT [alt]: the shared restart route. With alt it opens
# with a line saying this is the other way, and numbers from 1 again.
restart_steps() {
  if [[ "${3:-}" == alt ]]; then
    printf '\nOr restart the run instead:\n'
    _pn_n=0
  fi
  _pn_teardown "$1" "$2"
  _pn_handoff
}

# _pn_read_pane PANE TEXT: the step that reads an agent's pane.
_pn_read_pane() {
  _pn_step "$2" "herdr agent read ${1:-NAME} --source recent-unwrapped --lines 120"
}

# _pn_read_log TEXT: the step that reads the bridge log, where the error of a
# tick that could not start the task is written.
_pn_read_log() {
  if [[ -n "$_pn_mailbox" ]]; then
    _pn_step "$1" "grep -F 'was not started' $(printf %q "$_pn_mailbox/bridge.log") | tail -n 5"
  else
    _pn_step "$1 It is bridge.log in the relay folder."
  fi
}

# _pn_what CODE: why the run stopped, or nothing for an unknown code.
_pn_what() {
  case "$1" in
    check_failed)       echo "the check for the task failed." ;;
    review_failed)      echo "the reviewer reported that the task failed review." ;;
    review_unknown)     echo "no valid review result was recorded in time. A pane review waits 10 minutes, and a mailbox review waits 5." ;;
    stuck)              echo "the builder said it could not finish the task." ;;
    repairs_exhausted)  echo "the repairs for the task ran out and it is still not accepted." ;;
    writer_unknown)     echo "tandem cannot tell whether the builder received the task." ;;
    dispatch_failed)    echo "the task never reached a working builder." ;;
    agent_exited)       echo "the builder or reviewer CLI quit, so its pane no longer runs it." ;;
    agent_unsupported)  echo "the run uses an agent kind tandem no longer supports." ;;
    no_reviewer)        echo "the run has no reviewer pane, and the builder never reviews." ;;
    plan_changed)       echo "the plan file changed after the run started." ;;
    source_mutated)     echo "the files changed after tandem measured them. A person, another tool or a check that edits files can do this." ;;
    scheduling_blocked) echo "a budget limit or a held launch is blocking the next step." ;;
    context_too_large)  echo "the task's instructions are larger than the context limit." ;;
    context_incomplete) echo "the task's instructions could not be built, because evidence for them is incomplete." ;;
    memory_conflict)    echo "two recorded facts for the task contradict each other." ;;
    recovery_blocked)   echo "the task failed and the run cannot repair it by itself. Either the failure is not one it repairs automatically, or its repair allowance is used up." ;;
    verification_identity) echo "a review result came from the builder's or planner's own conversation, which counts for nothing." ;;
    verification_stale) echo "the review result no longer matches the attempt's patch and files, or its review job did not finish." ;;
    stopped)            echo "someone asked it to stop." ;;
  esac
}

_pn_agent_exited() {
  local q; q=$(printf %q "$2")
  _pn_step "Start the agent again in its pane with tandem-pair. The builder pane is ${3:-unknown} and the reviewer pane is ${4:-unknown}." "tandem-pair"
  _pn_step "Continue the run once the agent is back." "tandem build recover --root $q"
}

_pn_agent_unsupported() {
  local q; q=$(printf %q "$2")
  printf 'Tandem types nothing more into this run. If a supported agent in it quit, bring it back first with tandem-pair, because a stop waits for it.\n'
  printf '\nWhat to do:\n'
  _pn_step "Stop the run." "tandem build stop --root $q"
  _pn_step "Wait until the run is paused. A stop waits for the builder's current turn and the wait ticks the run, so it can take a few minutes. If it times out, run it again." \
    "tandem build wait --root $q --until paused --tick --timeout 120000"
  _pn_step "End its open jobs." "tandem state recover --run $1 --root $q"
  _pn_step "Archive the run." "tandem state archive --run $1 --root $q"
  _pn_step "Pair a supported agent (Codex, Claude or Copilot) with tandem-pair, then hand the plan off again." "tandem-pair"
  _pn_handoff
}

_pn_writer_unknown() {
  local q; q=$(printf %q "$2")
  _pn_read_pane "$4" "Look at the builder pane ${4:-NAME}. If the builder is working on the task, it read the prompt. Do step 2 once its turn has ended, and then you are done."
  _pn_step "Let tandem see that the prompt was read." "tandem state recover --run $1 --root $q"
  _pn_step "If the builder never read the prompt, clear what blocks it, such as a startup dialog or a provider error. Then export the run to a folder that does not exist yet. The output lists the unresolved deliveries on screen, each with an effect id. Pick the one for the builder's task prompt, not a reviewer prompt or a notice." \
    "tandem state export --run $1 --output /tmp/tandem-export-$1 --root $q"
  _pn_step "Record that the builder did not read the prompt." \
    "tandem state settle-delivery --run $1 --effect EFFECT --not-read --because WHY --root $q"
  restart_steps "$1" "$2"
}

# pause_note CODE RUN ROOT TASK BUILDER REVIEWER REASON PLAN COLLABORATION MAILBOX
pause_note() {
  local code="${1:-}" run="${2:-}" root="${3:-}" task="${4:-}" builder="${5:-}" reviewer="${6:-}" reason="${7:-}" collab="${9:-}"
  local q what; q=$(printf %q "$root")
  _pn_n=0; _pn_plan="${8:-}"; _pn_mailbox="${10:-}"
  what=$(_pn_what "$code")
  if [[ "$code" == stopping ]]; then
    printf '[build] Run %s is stopping: it is waiting for the builder'"'"'s current turn to end.\n' "$run"
  elif [[ -n "$what" ]]; then
    printf '[build] Run %s stopped: %s\n' "$run" "$what"
  elif [[ -z "$code" ]]; then
    printf '[build] Run %s stopped: it paused with no pause code.\n' "$run"
  else
    printf '[build] Run %s stopped: it paused with the code %s, which this tandem has no note for.\n' "$run" "$code"
  fi
  [[ -z "$task" ]] || printf 'Task: %s\n' "$task"
  [[ -z "$builder" ]] || printf 'Builder pane: %s\n' "$builder"
  [[ -z "$reviewer" ]] || printf 'Reviewer pane: %s\n' "$reviewer"
  [[ -z "$reason" ]] || printf '%s\n' "$reason"
  case "$code" in
    agent_unsupported) _pn_agent_unsupported "$run" "$root"; return 0 ;;
    no_reviewer) printf 'This run started before every run needed a reviewer pane.\n' ;;
  esac
  printf '\nWhat to do:\n'
  case "$code" in
    check_failed)
      if [[ -n "$reason" ]]; then
        _pn_step "Read the end of the check's log above. Fix the cause in the code or the check."
      else
        _pn_step "The check's log could not be read here. Run the task's check command yourself in the repo and read its output. Fix the cause in the code or the check."
      fi
      restart_steps "$run" "$root" ;;
    source_mutated)
      _pn_step "Look at what changed with git status. Commit any stray changes or set them aside, such as with git stash."
      restart_steps "$run" "$root" ;;
    review_failed)
      if [[ -n "$reason" ]]; then
        _pn_read_pane "$reviewer" "Read the findings above, and the reviewer pane for the rest."
      else
        _pn_read_pane "$reviewer" "Read the reviewer pane for what it found."
      fi
      restart_steps "$run" "$root" ;;
    review_unknown)
      _pn_read_pane "$reviewer" "Read the reviewer pane for its reply."
      _pn_step "Save the whole JSON object of the reply to a file, then record it." "tandem build review-record --result FILE --root $q"
      _pn_step "Or overrule the review instead." "tandem build accept --task ${task:-N} --waive review --because WHY --root $q"
      restart_steps "$run" "$root" alt ;;
    stuck)
      _pn_read_pane "$builder" "Read the builder pane for its report."
      restart_steps "$run" "$root" ;;
    repairs_exhausted)
      if [[ -n "$reason" ]]; then
        _pn_step "Read what kept failing, above."
      else
        _pn_read_pane "$reviewer" "Read the reviewer pane for what kept failing."
      fi
      restart_steps "$run" "$root" ;;
    writer_unknown) _pn_writer_unknown "$run" "$root" "$task" "$builder" ;;
    dispatch_failed)
      _pn_step "Fix the builder pane ${builder:-NAME}. A model the CLI rejects, an expired login or a used-up quota looks like this. Re-run tandem-pair if the pane is gone."
      _pn_step "If the pair has no reviewer: $REVIEWER_REMEDY"
      restart_steps "$run" "$root" ;;
    agent_exited) _pn_agent_exited "$run" "$root" "$builder" "$reviewer" ;;
    no_reviewer)
      _pn_teardown "$run" "$root"
      _pn_step "Pair a reviewer. $REVIEWER_REMEDY"
      _pn_handoff ;;
    plan_changed)
      _pn_step "Put the plan file back as it was, then continue the run." "tandem build recover --root $q"
      restart_steps "$run" "$root" alt ;;
    scheduling_blocked)
      _pn_step "If a budget stopped it, raise the limit. Use --scope run for the run's limit." "tandem build budget --scope task --limit N --root $q"
      if [[ -n "$collab" ]]; then
        _pn_step "If the collaboration holds the launch, resume it." "tandem collaboration resume --collaboration $collab --actor you --reason WHY --root $q"
      fi ;;
    context_too_large)
      _pn_teardown "$run" "$root"
      _pn_handoff "--context-limit N" ;;
    context_incomplete|memory_conflict)
      _pn_read_log "Read the error tandem logged when it could not build the task."
      restart_steps "$run" "$root" ;;
    recovery_blocked|verification_identity|verification_stale)
      if [[ -n "$reason" ]]; then
        _pn_step "Read the cause above."
      else
        _pn_step "Read where the task stands." "tandem build status --root $q"
      fi
      restart_steps "$run" "$root" ;;
    stopped)
      restart_steps "$run" "$root"
      printf '\nTo close the pair instead, run tandem-unpair.\n' ;;
    stopping)
      _pn_step "Nothing yet. Check again in a minute." "tandem build status --root $q" ;;
    *)
      _pn_step "Read why it stopped." "tandem build status --root $q" ;;
  esac
}

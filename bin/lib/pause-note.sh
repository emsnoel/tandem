# shellcheck shell=bash
# bin/lib/pause-note.sh: the note a person reads when a durable run pauses.
#
# Sourced by tandem-bridge, tandem-handoff and tandem-pause-note, never run.
# Needs REVIEWER_REMEDY from lib/reviewer.sh. The Go side reports facts (the
# code and the run's own record) and this file says what to do about them.
#
# Every `tandem` command in a note sits on its own line, indented four spaces,
# with the root quoted by printf %q so a path with spaces stays one argument.

_pn_n=0

# _pn_step TEXT [COMMAND]: one numbered step, then its command if it has one.
_pn_step() {
  _pn_n=$(( _pn_n + 1 ))
  printf '%d. %s\n' "$_pn_n" "$1"
  [[ -z "${2:-}" ]] || printf '    %s\n' "$2"
}

# _pn_teardown RUN ROOT: stop the run, end its open jobs, archive it.
_pn_teardown() {
  local q; q=$(printf %q "$2")
  _pn_step "Stop the run." "tandem build stop --root $q"
  _pn_step "End its open jobs. An open job blocks the archive." "tandem state recover --run $1 --root $q"
  _pn_step "Archive the run." "tandem state archive --run $1 --root $q"
}

# _pn_handoff FLAGS: hand the plan off again, which starts at task 1.
_pn_handoff() {
  _pn_step "Hand the plan off again. The run starts again at task 1." "tandem-handoff ${1:-}PLAN"
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

# _pn_waive ROOT TASK SCOPE TEXT: the step that overrules one requirement.
_pn_waive() {
  local q; q=$(printf %q "$1")
  _pn_step "$4" "tandem build accept --task ${2:-N} --waive $3 --because WHY --root $q"
}

# _pn_read_pane PANE TEXT: the step that reads an agent's pane.
_pn_read_pane() {
  _pn_step "$2" "herdr agent read ${1:-NAME} --source recent-unwrapped --lines 120"
}

# _pn_what CODE: the first line's reason, or nothing for an unknown code.
_pn_what() {
  case "$1" in
    check_failed)       echo "the check for the task failed." ;;
    review_failed)      echo "the reviewer reported that the task failed review." ;;
    review_unknown)     echo "no valid review result came back in 10 minutes." ;;
    stuck)              echo "the builder said it could not finish the task." ;;
    repairs_exhausted)  echo "the builder used up its repair attempts and the task is still not accepted." ;;
    writer_unknown)     echo "tandem cannot tell whether the builder received the task." ;;
    dispatch_failed)    echo "the task never reached a working builder." ;;
    agent_exited)       echo "the builder or reviewer CLI quit, so its pane no longer runs it." ;;
    agent_unsupported)  echo "the run uses an agent kind tandem no longer supports." ;;
    no_reviewer)        echo "the run has no reviewer pane, and the builder never reviews." ;;
    plan_changed)       echo "the plan file changed after the run started." ;;
    source_mutated)     echo "the files changed outside the run while it was working." ;;
    scheduling_blocked) echo "a budget limit or a held launch is blocking the next step." ;;
    context_too_large)  echo "the task's instructions are larger than the context limit." ;;
    context_incomplete) echo "the task's instructions could not be built from complete records." ;;
    memory_conflict)    echo "two recorded facts for the task contradict each other." ;;
    recovery_blocked)   echo "the task failed in a way the run does not repair by itself." ;;
    verification_identity) echo "a review result came from the builder's or planner's own conversation, which counts for nothing." ;;
    verification_stale) echo "the review result does not match the task's current files." ;;
    stopped)            echo "someone asked it to stop." ;;
    stopping)           echo "it is stopping and waits for the builder's current turn to end." ;;
  esac
}

# _pn_agent_exited / _pn_agent_unsupported: the two notes with their own steps.
_pn_agent_exited() {
  local q; q=$(printf %q "$2")
  _pn_step "Start the agent again in its pane with tandem-pair. The builder pane is ${3:-unknown}, the reviewer pane is ${4:-unknown}." "tandem-pair"
  _pn_step "Continue the run once the agent is back." "tandem build recover --root $q"
}

_pn_agent_unsupported() {
  local q; q=$(printf %q "$2")
  printf 'Tandem types nothing more into this run. If a supported agent in it quit, bring it back first with tandem-pair, because a stop waits for it.\n'
  printf '\nWhat to do:\n'
  _pn_step "Stop the run." "tandem build stop --root $q"
  _pn_step "Tick the run so the stop settles." "tandem build tick --root $q"
  _pn_step "End its open jobs." "tandem state recover --run $1 --root $q"
  _pn_step "Archive the run." "tandem state archive --run $1 --root $q"
  _pn_step "Pair a supported agent (Codex, Claude or Copilot) with tandem-pair, then hand the plan off again." "tandem-handoff PLAN"
}

_pn_writer_unknown() {
  local q; q=$(printf %q "$2")
  _pn_read_pane "$4" "Look at the builder pane ${4:-NAME}. If the builder is working on the task, it read the prompt, so wait and stop here."
  _pn_step "If it never read the prompt, clear what blocks it, such as a startup dialog or a provider error. Then write the run to a file and read the effect id under unresolved in it." \
    "tandem state export --run $1 --output FILE --root $q"
  _pn_step "Record that the builder did not read the prompt." \
    "tandem state settle-delivery --run $1 --effect EFFECT --not-read --because WHY --root $q"
  restart_steps "$1" "$2"
}

# pause_note CODE RUN ROOT TASK BUILDER REVIEWER REASON
pause_note() {
  local code="${1:-}" run="${2:-}" root="${3:-}" task="${4:-}" builder="${5:-}" reviewer="${6:-}" reason="${7:-}"
  local q what; q=$(printf %q "$root")
  _pn_n=0
  what=$(_pn_what "$code")
  if [[ -z "$what" ]]; then
    if [[ -z "$code" ]]; then
      printf '[build] Run %s stopped: it paused with no pause code.\n' "$run"
    else
      printf '[build] Run %s stopped: it paused with the code %s, which this tandem has no note for.\n' "$run" "$code"
    fi
  else
    printf '[build] Run %s stopped: %s\n' "$run" "$what"
  fi
  [[ -z "$task" ]] || printf 'Task: %s\n' "$task"
  [[ -z "$builder" ]] || printf 'Builder pane: %s\n' "$builder"
  [[ -z "$reviewer" ]] || printf 'Reviewer pane: %s\n' "$reviewer"
  [[ -z "$reason" ]] || printf '%s\n' "$reason"
  if [[ "$code" == agent_unsupported ]]; then
    _pn_agent_unsupported "$run" "$root"
    return 0
  fi
  printf '\nWhat to do:\n'
  case "$code" in
    check_failed)
      _pn_step "Read what the check reported." "tandem build status --root $q"
      _pn_waive "$root" "$task" check "If the task is fine, overrule the check."
      restart_steps "$run" "$root" alt ;;
    review_failed)
      _pn_read_pane "$reviewer" "Read the reviewer pane for what it found."
      _pn_waive "$root" "$task" review "If the findings are wrong, overrule the review."
      restart_steps "$run" "$root" alt ;;
    review_unknown)
      _pn_read_pane "$reviewer" "Read the reviewer pane for its reply."
      _pn_step "Save the whole JSON object of the reply to a file, then record it." "tandem build review-record --result FILE --root $q"
      _pn_waive "$root" "$task" review "Or overrule the review instead."
      restart_steps "$run" "$root" alt ;;
    stuck)
      _pn_read_pane "$builder" "Read the builder pane for its report."
      _pn_waive "$root" "$task" implementation "If the task is in fact done, overrule the builder."
      restart_steps "$run" "$root" alt ;;
    repairs_exhausted)
      _pn_step "Read the findings that kept failing." "tandem build status --root $q"
      _pn_waive "$root" "$task" SCOPE "If the task is fine, overrule the requirement that keeps failing. SCOPE is check, review or implementation."
      restart_steps "$run" "$root" alt ;;
    writer_unknown) _pn_writer_unknown "$run" "$root" "$task" "$builder" ;;
    dispatch_failed)
      _pn_step "Fix the builder pane ${builder:-NAME}. A model the CLI rejects, an expired login or a used-up quota looks like this. Re-run tandem-pair if the pane is gone."
      restart_steps "$run" "$root" ;;
    agent_exited) _pn_agent_exited "$run" "$root" "$builder" "$reviewer" ;;
    no_reviewer)
      printf 'This run started before every run needed a reviewer pane.\n'
      _pn_teardown "$run" "$root"
      _pn_step "Pair a reviewer. $REVIEWER_REMEDY"
      _pn_handoff ;;
    plan_changed)
      _pn_step "Put the plan file back as it was, then continue the run." "tandem build recover --root $q"
      restart_steps "$run" "$root" alt ;;
    source_mutated) restart_steps "$run" "$root" ;;
    scheduling_blocked)
      _pn_step "If a budget stopped it, raise the limit. Use --scope run for the run's limit." "tandem build budget --scope task --limit N --root $q"
      _pn_step "If a collaboration holds the launch, resume it." "tandem collaboration resume --collaboration ID --actor you --reason WHY --root $q" ;;
    context_too_large)
      _pn_teardown "$run" "$root"
      _pn_handoff "--context-limit N " ;;
    context_incomplete|memory_conflict|recovery_blocked|verification_identity|verification_stale)
      _pn_step "Read the task's record." "tandem build status --root $q"
      restart_steps "$run" "$root" ;;
    stopped)
      restart_steps "$run" "$root"
      printf '\nTo close the pair instead, run tandem-unpair.\n' ;;
    stopping)
      _pn_step "Nothing yet. Wait for the builder's turn to end, then check the run." "tandem build status --root $q" ;;
    *)
      _pn_step "Read why it stopped." "tandem build status --root $q" ;;
  esac
}

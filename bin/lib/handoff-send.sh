# bin/lib/handoff-send.sh: send the task to the builder and record where it went.
#
# Sourced by bin/tandem-handoff, never run. The functions read and set its
# globals; tandem makes the decisions and returns NUL fields in $FIELDS.

# bin/tandem-handoff sets and reads the globals, which shellcheck cannot see
# from this file alone.
# shellcheck shell=bash disable=SC2034,SC2153,SC2154

# say_reset TOKEN prints the sentence for a reset token, or the raw token
# when this tandem is too old to explain it.
say_reset() {
  local s
  s=$(tandem agent explain-reset "$1" --for build 2>/dev/null) || s=""
  info "${s:-builder conversation: ${1:-no answer}}"
}

# turn_ended_at_once WHAT NOTE [WHO PANE]: `agent prompt --as` exited 26, so the
# turn ended as soon as it started. _pres holds the message and the pane screen.
turn_ended_at_once() {
  cat >&2 <<MSG
tandem-handoff: WARNING the ${3:-builder}'s turn ended as soon as it started, so nothing is working on $1.

  Pane ${4:-$BUILDER_PANE} shows:

$(printf '%s\n' "$_pres" | tail -n +2 | sed 's/^/    /')

  A model the CLI rejects, an expired login or used-up quota looks like this.
  Fix it in that pane (or re-run tandem-pair), then re-run this command. $2
MSG
}


# record_build_session: record the task as sent and the conversation holding it.
record_build_session() {
  local s
  s=$(tandem agent session "$BUILDER_NAME" --not "$PRE_SESSION" --timeout 5000 2>/dev/null) || s=""
  # Record which conversation holds this task, so its token use can be read
  # from that conversation's log. --not: Codex's id moves only once the first
  # turn starts.
  tandem build sent --root "$ROOT" --task "$TASK" ${s:+--session "$s"} >/dev/null 2>&1 \
    || info "WARNING could not record task $TASK as sent in the build record."
  [[ -n "$s" ]] || return 0
  # An id that did not move after a reset is still the review's conversation.
  # Recording it would make the next handoff skip a reset it needs.
  if [[ "$RESET" == reset || "$RESET" == sent ]] && [[ "$s" == "$PRE_SESSION" ]]; then
    info "WARNING the builder's session did not change when the build started; it may be carrying the review conversation."
    return 0
  fi
  { printf '%s\n' "$s" > "$MAILBOX/build-session.tmp" && mv "$MAILBOX/build-session.tmp" "$MAILBOX/build-session"; } \
    || info "WARNING could not record $MAILBOX/build-session; a later handoff may reset this build's conversation."
}

# send_task: print what is about to happen, then reset the builder and send
# the prompt, or print it under --dry-run.
send_task() {
  printf 'tandem-handoff: builder %s (pane %s, status: %s)\n' "$BUILDER_NAME" "$BUILDER_PANE" "$BUILDER_STATUS"
  printf 'tandem-handoff: project %s (%s)\n' "$ROOT" "$PROJ_SOURCE"
  printf 'tandem-handoff: plan    %s\n' "$PLAN_ABS"
  _resumed=""
  # --continue is the bridge sending the next task, not a resume after a stop.
  if [[ "$RESUMED" == true ]] && (( ! CONTINUE )); then
    _resumed=" (resuming a build that stopped part-way)"
  fi
  printf 'tandem-handoff: task    %s of %s: %s%s\n' "$TASK" "$TOTAL" "$TASK_TITLE" "$_resumed"
  printf 'tandem-handoff: brief   %s\n' "$BRIEF"

  if (( DRY_RUN )); then
    say_reset "$(tandem agent reset "$BUILDER_NAME" --root "$ROOT" --dry-run 2>/dev/null || true)"
    printf '\n--- prompt that would be sent ---\n%s\n--- end ---\n' "$LAUNCH_PROMPT"
    exit 0
  fi

  if [[ "$BUILDER_STATUS" == blocked ]]; then
    die "builder is blocked at an approval or question dialog. Inspect pane $BUILDER_PANE and clear it first."
  fi

  # The review's findings are already in the plan, so the build starts from an
  # empty conversation. A skipped reset only costs tokens.
  PRE_SESSION=$(tandem agent session "$BUILDER_NAME" 2>/dev/null) || PRE_SESSION=""
  RESET=$(tandem agent reset "$BUILDER_NAME" --root "$ROOT" 2>/dev/null) || RESET=""
  say_reset "$RESET"
  # `unconfirmed`: nothing visible followed the reset, even after the confirm
  # key. The pane may be at a dialog that would swallow the prompt, so send
  # nothing.
  if [[ "$RESET" == unconfirmed ]]; then
    cat >&2 <<MSG
tandem-handoff: WARNING the builder's reset could not be confirmed, so the plan was NOT sent.

  $BUILDER_NAME may be at a native dialog Herdr cannot see, or still busy.
  Check pane $BUILDER_PANE, clear any dialog, then re-run this command.
MSG
    exit 7
  fi

  PFLAGS=()
  (( WAIT )) && PFLAGS+=( --wait --timeout "$PROMPT_TIMEOUT_MS" )

  # `agent prompt` returning 0 does not prove delivery: Herdr reports idle for a
  # pane at a native dialog, and a rejected model ends its turn at once. --as
  # watches the pane after typing (cmd/tandem/confirm_start.go). --wait waits for
  # the turn to settle instead. Without --wait the result is on stdout or stderr,
  # so 2>&1 captures it.
  rc=0
  if (( WAIT )); then
    _pres=$(tandem agent prompt "$BUILDER_NAME" "$LAUNCH_PROMPT" "${PFLAGS[@]}" 2>&1 >/dev/null) || rc=$?
  else
    _pres=$(tandem agent prompt "$BUILDER_NAME" "$LAUNCH_PROMPT" --as task 2>&1) || rc=$?
  fi
  IFS=$'\t' read -r _code _msg <<<"$_pres"

  case "$rc" in
    0)
      if (( WAIT )); then
        info "plan handed to '$BUILDER_NAME'."
        info "builder settled at: $(tandem agent status "$BUILDER_NAME" 2>/dev/null || printf 'unknown')"
      else
        info "plan handed to '$BUILDER_NAME' (now $_msg)."
      fi
      record_build_session
      exit 0 ;;
    25)
      cat >&2 <<MSG
tandem-handoff: WARNING the prompt was accepted but $BUILDER_NAME never started working.

  It is most likely sitting at a native dialog that Herdr cannot see, which
  swallows input while still reporting idle. Check pane $BUILDER_PANE, clear
  any dialog, then re-run this command. Do not assume the plan was delivered.
MSG
      exit 7 ;;
    26)
      turn_ended_at_once "the plan" "Do not assume the plan was delivered."
      exit 8 ;;
  esac

  case "$_code" in
    agent_blocked)        die "builder is waiting at an approval or question dialog; nothing was sent. Inspect pane $BUILDER_PANE." ;;
    agent_input_elsewhere) die "$_msg. Return pane $BUILDER_PANE to its main conversation, then re-run this command." ;;
    agent_prompt_stalled) die "prompt sent but the builder showed no lifecycle change. Inspect pane $BUILDER_PANE." ;;
    *)                    die "could not prompt the builder: ${_msg:-unknown error}" ;;
  esac
}

# shellcheck shell=bash
# bin/lib/review-deliver.sh: sourced by bin/tandem-review. send_review resets
# the reviewer and sends the prompt, then says what happened. It reads the
# script's globals and always exits.

# send_review: reset the reviewer's conversation, send REVIEW_PROMPT, and exit
# with the outcome.
send_review() {
  # The previous round's findings are in its review file, so the review starts
  # from an empty conversation. A skipped reset only costs tokens. The reset uses
  # the reviewer's own kind.
  RESET=$(tandem agent reset "$TARGET_NAME" --root "$ROOT" 2>/dev/null) || RESET=""
  say_reset "$RESET"
  # `unconfirmed`: nothing visible followed the reset. The pane may be at a
  # dialog that would swallow the prompt, so send nothing.
  if [[ "$RESET" == unconfirmed ]]; then
    cat >&2 <<MSG
tandem-review: WARNING the $TARGET_ROLE's reset could not be confirmed, so NOTHING WAS SENT.

  $TARGET_NAME may be at a native dialog Herdr cannot see, or still busy.
  Check pane $TARGET_PANE, clear any dialog, then re-run this command.
MSG
    exit 7
  fi

  # `agent prompt` returning 0 does not prove delivery. --as watches the pane
  # (cmd/tandem/confirm_start.go): 25 means a dialog swallowed the prompt, 26
  # means the turn ended at once. The result is on stdout or stderr, never both.
  rc=0
  _pres=$(tandem agent prompt "$TARGET_NAME" "$REVIEW_PROMPT" --as review 2>&1) || rc=$?
  IFS=$'\t' read -r _code _msg <<<"$_pres"
  case "$rc" in
    0)
      if [[ "$_code" == queued ]]; then
        info "review prompt accepted, but $TARGET_NAME was already $_msg, so tandem could not confirm delivery."
      else
        info "review requested ($TARGET_ROLE now $_msg)."
      fi
      info "the verdict arrives as a message in this pane; no polling needed."
      info "to check by hand: tandem-status $PLAN_ABS"
      exit 0 ;;
    25)
      cat >&2 <<MSG
tandem-review: WARNING the prompt was accepted but $TARGET_NAME never started.

  It is most likely at a native dialog Herdr cannot see, which swallows input
  while still reporting settled. Check pane $TARGET_PANE, clear it, and re-run.
MSG
      exit 7 ;;
    26)
      cat >&2 <<MSG
tandem-review: WARNING the $TARGET_ROLE's turn ended as soon as it started, so nothing is reviewing the plan.

  Pane $TARGET_PANE shows:

$(printf '%s\n' "$_pres" | tail -n +2 | sed 's/^/    /')

  A model the CLI rejects, an expired login or used-up quota looks like this.
  Fix it in that pane (or re-run tandem-pair), then re-run this command.
MSG
      exit 8 ;;
  esac
  case "$_code" in
    agent_blocked)        die "$TARGET_ROLE is waiting at an approval or question dialog; nothing was sent. Inspect pane $TARGET_PANE." ;;
    agent_input_elsewhere) die "$_msg. Return pane $TARGET_PANE to its main conversation, then re-run this command." ;;
    agent_prompt_stalled) die "prompt sent but the $TARGET_ROLE showed no lifecycle change. Inspect pane $TARGET_PANE." ;;
    *)                    die "could not prompt the $TARGET_ROLE: ${_msg:-unknown error}. NOTHING WAS SENT." ;;
  esac
}

# bin/lib/handback-deliver.sh: tell the planner the work is back.
#
# Sourced by bin/tandem-handback, never run. The functions read and set its globals.

# bin/tandem-handback sets and reads the globals, which shellcheck cannot see from
# this file alone.
# shellcheck shell=bash disable=SC2034,SC2153,SC2154

# One confirmed delivery: wait out a dialog, type, then confirm the planner's
# turn started (cmd/tandem/confirm_start.go).
undelivered() {
  cat >&2 <<MSG
tandem-handback: WARNING the prompt was accepted but $PLANNER_NAME never woke up.

  It is most likely sitting at a native dialog that Herdr cannot see, which
  swallows input while still reporting idle, or its turn ended at once. Your work
  is done and your report is safe at:

    $REPORT_PATH

  Do NOT redo the work and do NOT send it again. Say in your next message that the
  handback was not delivered, and give the human that path.
MSG
  if [[ -n "${1:-}" ]]; then
    printf '\n  The planner pane shows:\n\n' >&2
    printf '%s\n' "$1" | sed 's/^/    /' >&2
  fi
  exit 7
}

# ==============================================================================
# HANDBACK_PROMPT: the contract handed to the planner when work comes back.
#
# A quoted heredoc like LAUNCH_PROMPT, so paths are never evaluated by the shell.
# It tells the planner to:
#   1. Treat the report as the builder's claim, not proof, and check the tree.
#   2. Prefer rework, and tear down only as a deliberate last step.
# ==============================================================================
# build_message: fill MESSAGE from bin/lib/handback-prompt.txt.
build_message() {
  [[ -r "$HANDBACK_LIB/handback-prompt.txt" ]] \
    || die "the prompt template $HANDBACK_LIB/handback-prompt.txt is missing; re-run install.sh. Nothing was sent."
  read -r -d '' TEMPLATE < "$HANDBACK_LIB/handback-prompt.txt" || true

  # A leading newline keeps the template's blank line when there is no plan.
  [[ -n "$PLAN_PATH" ]] && PLANLINE=$'\nPlan:   '"$PLAN_PATH" || PLANLINE=""

  MESSAGE="$TEMPLATE"
  MESSAGE="${MESSAGE//\{\{SELF\}\}/$SELF}"
  MESSAGE="${MESSAGE//\{\{PLANLINE\}\}/$PLANLINE}"
  MESSAGE="${MESSAGE//\{\{REPORT_PATH\}\}/$REPORT_PATH}"
  MESSAGE="${MESSAGE//\{\{REPORT_ID\}\}/$ID}"
}

# print_dry_run: what a real handback would write and send.
print_dry_run() {
  if (( DRY_RUN )); then
    printf 'tandem-handback: from %s to %s\n' "$SELF" "$PLANNER_NAME"
    printf '  [dry-run] write report  %s (%s lines)\n' "$REPORT_PATH" "$(printf '%s\n' "$REPORT" | wc -l | tr -d ' ')"
    rc=0
    tandem agent snapshot "$PLANNER_NAME" >/dev/null 2>&1 || rc=$?
    if (( rc != 3 )); then
      printf '  [dry-run] socket reachable -> herdr agent prompt %s\n' "$PLANNER_NAME"
    else
      printf '  [dry-run] socket unreachable -> mailbox %s/q/%s.json\n' "$MAILBOX" "$ID"
    fi
    printf '\n--- message that would be sent ---\n%s\n--- end ---\n' "$MESSAGE"
    exit 0
  fi
}

# deliver_handback: write the report, then prompt the planner or queue the
# handback for the bridge when the socket is unreachable.
deliver_handback() {
  mkdir -p "$MAILBOX/q" "$MAILBOX/a" "$MAILBOX/reports" \
    || die "cannot create the relay directory $MAILBOX"

  # Write then move, so the planner never reads a partial report.
  printf '%s\n' "$REPORT" > "$REPORT_PATH.tmp" || die "could not write the report file."
  mv "$REPORT_PATH.tmp" "$REPORT_PATH"
  info "report written to $REPORT_PATH"

  # --- deliver ---
  # Use the mailbox when the socket is unreachable, as tandem-ask does. Snapshot
  # exit 3 means this sandbox cannot reach the socket; other failures mean the
  # planner is gone.
  rc=0
  _snap=$(tandem agent snapshot "$PLANNER_NAME" 2>/dev/null) || rc=$?
  if (( rc == 3 )); then
    QFILE="$MAILBOX/q/$ID.json"
    # The bridge delivers this message as written.
    tandem relay write handback --mailbox "$MAILBOX" --id "$ID" --from "$SELF" --to "$PLANNER_NAME" \
      --message "$MESSAGE" --report "$REPORT_PATH" --plan "$PLAN_PATH" \
      || die "could not write the handback file."
    info "socket unreachable (sandbox), so the handback was queued at $QFILE."
    info "the bridge will deliver it to '$PLANNER_NAME'. You are done; do not wait."
    exit 0
  fi

  if (( rc != 0 )); then
    cat >&2 <<MSG
tandem-handback: no agent named '$PLANNER_NAME'; the handback was not delivered.

  Your report is safe at:

    $REPORT_PATH

  Tell the human it is there, and that '$PLANNER_NAME' is gone. Do not redo the work.
MSG
    exit 2
  fi
  IFS=$'\t' read -r PLANNER_PANE _ _ <<<"$_snap"


  rc=0
  _perr=$(tandem agent prompt "$PLANNER_NAME" "$MESSAGE" --as handback \
            --wait-unblocked "$(( BLOCKED_WAIT_SECONDS * 1000 ))" 2>&1 >/dev/null) || rc=$?
  IFS=$'\t' read -r _code _msg <<<"$(printf '%s' "$_perr" | head -1)"
  if (( rc == 10 )) && [[ "$_code" == agent_blocked || "$_code" == agent_input_elsewhere ]]; then
    _why="was still blocked"
    # A pane showing a subagent would hand the report to that subagent.
    [[ "$_code" == agent_input_elsewhere ]] && _why="still showed something other than its conversation (${_msg})"
    cat >&2 <<MSG
tandem-handback: $PLANNER_NAME $_why after ${BLOCKED_WAIT_SECONDS}s; nothing was sent.

  Your report is safe at:

    $REPORT_PATH

  Ask the human to clear pane $PLANNER_PANE, then re-run this command.
MSG
    exit 3
  fi
  case "$rc" in
    0) ;;
    25) undelivered ;;
    26) undelivered "$(printf '%s' "$_perr" | tail -n +2)" ;;
    *)
      cat >&2 <<MSG
tandem-handback: could not deliver the handback to $PLANNER_NAME (${_msg:-unknown error}).

  Your report is safe at:

    $REPORT_PATH

  Tell the human that path in your next message rather than redoing the work.
MSG
      exit 5 ;;
  esac
}

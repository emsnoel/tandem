# bin/lib/unpair-refusals.sh: explain why tandem-unpair closed nothing.
#
# Sourced by bin/tandem-unpair, never run. The functions read and set its
# globals; tandem makes the decisions and returns NUL fields in $FIELDS.

# bin/tandem-unpair sets and reads the globals, which shellcheck cannot see
# from this file alone.
# shellcheck shell=bash disable=SC2034,SC2153,SC2154

# refuse_never_handed_back: gate 1, the builder never handed back.
refuse_never_handed_back() {
  if [[ -n "$DURABLE_ID" ]]; then
    cat >&2 <<MSG
tandem-unpair: durable run $DURABLE_ID has no task accepted; there is nothing to verify, so nothing was closed.

  See where it got to:   tandem build status --root $MB_ROOT
  Close it anyway:       tandem-unpair --force
MSG
    exit 4
  fi
  cat >&2 <<MSG
tandem-unpair: '$BUILDER_NAME' has never handed back; there is nothing to verify.

  An idle builder may not be done. It may have crashed, hit an error it could
  not report, or never had a plan. Its screen still shows what it was doing,
  and closing the pane deletes that. Check pane $BUILDER_PANE first.

  Ask it directly:   tandem-ask $BUILDER_NAME "where did you get to?"
  Close it anyway:   tandem-unpair --force
MSG
  exit 4
}

# refuse_unsigned: gate 2, work not signed off. $detail lists it.
refuse_unsigned() {
  if [[ -n "$DURABLE_ID" ]]; then
    printf 'tandem-unpair: durable run %s has tasks without the evidence to sign them off; nothing was closed.\n\n' "$DURABLE_ID" >&2
    # Each detail line is a task id, then what it lacks.
    while read -r _task _lacks; do
      [[ -n "$_task" ]] && printf '    %s  lacks: %s\n' "$_task" "$_lacks" >&2
    done <<<"$detail"
    cat >&2 <<MSG

  A task is signed off when it is accepted with a passed check and a recorded
  review for its final tree, or a waiver of either. See each task with:

    tandem build status --root $MB_ROOT

  Finish the run, or overrule a requirement you have checked yourself:

    tandem build accept --root $MB_ROOT --task N --waive check|review|implementation --because "why"

  Or close anyway with --force, which keeps the run's state.
MSG
    exit 5
  fi
  _pending_count=$(printf '%s\n' "$detail" | awk 'NF { n++ } END { print n+0 }')
  printf 'tandem-unpair: %s handback(s) not signed off; nothing was closed.\n\n' "$_pending_count" >&2
  while IFS= read -r _id; do
    [[ -n "$_id" ]] && printf '    %s\n' "$MAILBOX/reports/$_id.md" >&2
  done <<<"$detail"
  cat >&2 <<MSG

  Read each report against the working tree. A report is the builder's own
  account of its work, not proof of it. Then sign off what you have checked:

MSG
  printf '    tandem-unpair' >&2
  while IFS= read -r _id; do
    [[ -n "$_id" ]] && printf -- ' --verified %s' "$_id" >&2
  done <<<"$detail"
  printf '\n\n  If the work is not right, do not sign it off. Send it back instead.\n' >&2
  printf '  %s is idle and still has the context:\n\n' "$BUILDER_NAME" >&2
  printf '    tandem-ask %s "what needs fixing"\n' "$BUILDER_NAME" >&2
  exit 5
}

# refuse_no_check: gate 6, no check_command to verify with.
refuse_no_check() {
  cat >&2 <<MSG
tandem-unpair: no check_command for $ROOT, so the work cannot be verified here.

  Without a check, a sign-off is only your word. Set check_command in
  $ROOT/.tandem.json (tandem-init detects one), or say that you are closing
  without a check:

    tandem-unpair${VERIFIED[0]:+ --verified ${VERIFIED[0]}} --skip-checks
MSG
  exit 6
}

# refuse_moved: gate 4, something moved while the check ran. The first line
# of $detail names what moved.
refuse_moved() {
  _kind=$(printf '%s\n' "$detail" | head -1)
  _rest=$(printf '%s\n' "$detail" | tail -n +2)
  case "$_kind" in
    reports)
      cat >&2 <<MSG
tandem-unpair: a handback arrived or changed while the check was running; nothing was closed.

MSG
      while IFS= read -r _id; do
        [[ -n "$_id" ]] && printf '    %s\n' "$MAILBOX/reports/$_id.md" >&2
      done <<<"$_rest"
      cat >&2 <<MSG

  The check started before that handback, so its pass says nothing about the
  work in it. Read the handback, then run this again:

MSG
      printf '    tandem-unpair' >&2
      while IFS= read -r _id; do
        [[ -n "$_id" ]] && printf -- ' --verified %s' "$_id" >&2
      done <<<"$_rest"
      printf '\n' >&2 ;;
    status)
      printf 'tandem-unpair: builder went %s while the check ran; nothing was closed.\n\n' "$_rest" >&2
      printf '  It may be changing the files the check passed on, so that pass may no\n' >&2
      printf '  longer hold. Wait for it to go idle, then run this again.\n' >&2 ;;
    tree)
      cat >&2 <<MSG
tandem-unpair: the working tree changed while the check ran; nothing was closed.

  The check passed on a tree that has since changed, so it says nothing about
  what you would close. Run this command again to check again, or pass --force
  if you know the change does not matter.
MSG
      ;;
  esac
  exit 8
}

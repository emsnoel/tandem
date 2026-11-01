# bin/lib/handoff-gate.sh: the review gate a plan passes before the builder sees it.
#
# Sourced by bin/tandem-handoff, never run. The functions read and set its
# globals; tandem makes the decisions and returns NUL fields in $FIELDS.

# bin/tandem-handoff sets and reads the globals, which shellcheck cannot see
# from this file alone.
# shellcheck shell=bash disable=SC2034,SC2153,SC2154

# record_override: store --override-review's ruling once the gate confirms
# the named review is the newest.
# The ruling is stored in the review sidecar, so it stands until the plan
# changes. It is written only after the gate confirms the named review is the
# newest, since the gate never reads a superseded one.
record_override() {
  if [[ -n "$OVERRIDE_ID" ]]; then
    # Review ids have a fixed shape (minted by bin/tandem-handback) and are used
    # in file paths, so check the shape first.
    [[ "$OVERRIDE_ID" =~ ^[0-9]{8}-[0-9]{6}-[0-9]+-[0-9]+$ ]] \
      || die "--override-review takes a review id like 20260913-101530-4821-19273, not '$OVERRIDE_ID'. See $MAILBOX/reviews/."
    _meta="$MAILBOX/reviews/$OVERRIDE_ID.json"
    [[ -f "$_meta" ]] || die "no review with id $OVERRIDE_ID. Check $MAILBOX/reviews/."

    pre_rc=0
    _pre=$(tandem gate review --plan "$PLAN_ABS" --root "$ROOT" \
             --expect-review "$OVERRIDE_ID" 2>/dev/null) || pre_rc=$?
    # Line 1 is a refusal reason only when the gate refused. On success it is the
    # review id.
    _pre_reason=""
    (( pre_rc == 0 )) || _pre_reason=$(printf '%s' "$_pre" | sed -n '1p')
    if [[ "$_pre_reason" == superseded ]]; then
      _newest=$(printf '%s' "$_pre" | sed -n '2p')
      cat >&2 <<MSG
tandem-handoff: this plan has not cleared review.

  THE PLAN WAS NOT DELIVERED.

  $PLAN_ABS

  A NEWER review exists, so the one you named no longer counts. Nothing was
  sent and your reason was NOT recorded. Read the newer review before you
  overrule it:

      $MAILBOX/reviews/$_newest.md

  Then overrule that one:

      tandem-handoff --override-review $_newest --because "..."
MSG
      exit 24
    fi

    # The pre-check runs the full gate before the reason is written, so a current
    # review with objections reports "objections" here. The ruling is written only
    # for "objections" or a bare pass, and only for an exit code this script
    # understands (0 or 24). Any other result means no review matches this plan or
    # builder, or the binary is too old, so nothing is written and the main gate
    # call below prints the remedy.
    case "$_pre_reason" in
      plan|builder|absent|no_reviewer|builder_review|reviewer)
        printf 'tandem-handoff: the named review is no longer current; the ruling was NOT recorded.\n' >&2
        ;;
      objections|"")
        # An empty reason means the gate passed: either the review is clean or an
        # earlier ruling already cleared it. A ruling against a clean review would
        # record a decision nobody made.
        tandem gate ruling --relay "$MAILBOX" --id "$OVERRIDE_ID" >"$FIELDS" 2>/dev/null || true
        read_field _stored_verdict <"$FIELDS"
        if [[ -z "$_pre_reason" && "${_stored_verdict:-}" == clean ]]; then
          printf 'tandem-handoff: review %s raised no objections, so there is nothing to\n' "$OVERRIDE_ID" >&2
          printf '  overrule; the ruling was NOT recorded. Handing the plan over as usual.\n' >&2
        elif [[ "$pre_rc" != 0 && "$pre_rc" != 24 ]]; then
          printf 'tandem-handoff: the review gate returned an outcome this script does not\n' >&2
          printf '  understand (rc %s); the ruling was NOT recorded. The installed tandem may\n' "$pre_rc" >&2
          printf '  be behind these commands. Re-run install.sh.\n' >&2
        elif (( DRY_RUN )); then
          # A dry run changes nothing, and this write is the gate's only side effect.
          printf 'tandem-handoff: [dry-run] would record in %s:\n' "$_meta" >&2
          printf '  override_reason: %s\n' "$OVERRIDE_REASON" >&2
          # The skipped write is what the main gate call would accept, so skip that
          # call too and proceed as the override would.
          DRY_OVERRIDE_OK=1
        else
          tandem gate rule --relay "$MAILBOX" --id "$OVERRIDE_ID" --because "$OVERRIDE_REASON" \
            || die "could not record the override in $_meta"
        fi
        ;;
      *)
        printf 'tandem-handoff: the review gate returned an outcome this script does not\n' >&2
        printf '  understand (%s); the ruling was NOT recorded.\n' "$_pre_reason" >&2
        ;;
    esac
  fi
}

# review_gate: refuse, and request a review when one would clear the plan,
# unless the plan has cleared review.
review_gate() {
  rc=0
  _gate=""
  if (( DRY_RUN )) && (( DRY_OVERRIDE_OK )); then
    info "[dry-run] override of $OVERRIDE_ID would clear the review gate; nothing recorded."
  else
    _gate=$(tandem gate review --plan "$PLAN_ABS" --root "$ROOT" 2>/dev/null) || rc=$?
  fi
  if (( rc != 0 )); then
    # The reason, then the reviewed and current sides, then the review id last.
    GATE_REASON=$(printf '%s\n' "$_gate" | sed -n '1p')
    GATE_ID=$(printf '%s\n' "$_gate" | sed -n '2,$p' | tail -n 1)
    GATE_WAS=$(printf '%s\n' "$_gate" | sed -n '2p')
    GATE_NOW=$(printf '%s\n' "$_gate" | sed -n '3p')
    REVIEW_MD="$MAILBOX/reviews/$GATE_ID.md"

    # Each branch says for itself whether anything was sent.
    printf 'tandem-handoff: this plan has not cleared review.\n\n' >&2
    printf '  THE PLAN WAS NOT DELIVERED.\n\n' >&2
    printf '  %s\n' "$PLAN_ABS" >&2

    # absent, plan and builder all mean no review matches what is about to be
    # built, so request one. Only objections need the planner's decision.
    NEEDS_REVIEW=0
    case "$GATE_REASON" in
      absent)
        printf '\n  No review exists for it.\n' >&2
        NEEDS_REVIEW=1 ;;
      plan)
        printf '\n  The plan has changed since it was reviewed:\n\n' >&2
        diff -u "$MAILBOX/reviews/$GATE_ID.plan.md" "$PLAN_ABS" 2>/dev/null \
          | sed 's/^/      /' >&2 || true
        NEEDS_REVIEW=1 ;;
      no_reviewer)
        printf '\n  This pair has no reviewer, and the builder never reviews, so no\n' >&2
        printf '  review can clear this plan. %s\n' "$REVIEWER_REMEDY" >&2
        printf '  Then hand off again. Nothing was sent.\n' >&2 ;;
      builder_review)
        printf '\n  The review of this plan was written by the builder. The builder\n' >&2
        printf '  never reviews, so it does not count. A reviewer is being asked to\n' >&2
        printf '  review it again.\n' >&2
        NEEDS_REVIEW=1 ;;
      reviewer)
        printf '\n  The reviewer is not the one that reviewed this plan. A review does\n' >&2
        printf '  not survive a change of the reviewer kind or model.\n' >&2
        printf '\n  reviewed by: %s\n' "$GATE_WAS" >&2
        printf '  now:         %s\n' "$GATE_NOW" >&2
        NEEDS_REVIEW=1 ;;
      builder)
        if [[ -f "$MAILBOX/builder.json" ]]; then
          # Only said here: with no record there is no previous builder to compare.
          printf '\n  The builder is not the one that reviewed this plan. A review of\n' >&2
          printf '  what you can DO here does not survive a change of kind or autonomy.\n' >&2
          printf '\n  reviewed as: %s\n' "$GATE_WAS" >&2
          printf '  now:         %s\n' "$GATE_NOW" >&2
          NEEDS_REVIEW=1
        else
          # With no record, a re-review cannot succeed until tandem-pair records the
          # builder again, so say that instead of requesting one.
          printf '\n  No builder identity is recorded at all, so there is nothing to\n' >&2
          printf '  compare against. Run tandem-pair, then hand off again. A review\n' >&2
          printf '  requested now would fail the same way, at tandem-verdict.\n' >&2
        fi
        ;;
      objections)
        printf '\n  Nothing was sent; the builder has not seen this plan.\n\n' >&2
        printf '  review: %s\n\n' "$REVIEW_MD" >&2
        sed -n '1,40p' "$REVIEW_MD" 2>/dev/null | sed 's/^/  /' >&2 || true
        printf '\n  Fix the plan and hand off again. A changed plan voids the review, so\n' >&2
        printf '  it is reviewed again:\n\n' >&2
        printf '      $EDITOR %s\n' "$PLAN_ABS" >&2
        printf '\n  Or overrule them, saying why. The reason reaches the builder:\n\n' >&2
        printf '      tandem-handoff --override-review %s \\\n' "$GATE_ID" >&2
        printf '        --because "..."\n' >&2 ;;
      *)
        printf '\n  The review gate refused: %s\n' "${GATE_REASON:-unknown}" >&2 ;;
    esac

    if (( NEEDS_REVIEW )); then
      if (( DRY_RUN )); then
        printf '\n  [dry-run] would run: tandem-review %s\n' "$PLAN_ABS" >&2
        exit 24
      fi
      # Use tandem-review rather than a second copy of the review prompt.
      printf '\n  Requesting a review. The reviewer is being asked to READ this\n' >&2
      printf '  plan, not to build it.\n\n' >&2
      rc=0
      tandem-review "$PLAN_ABS" >&2 || rc=$?
      if (( rc != 0 )); then
        printf '\n  tandem-review failed (rc %s). Nothing was sent and no review\n' "$rc" >&2
        printf '  was requested. Fix that before handing off.\n' >&2
        exit "$rc"
      fi
      printf '\n  The reviewer will wake you with its verdict. Run this handoff\n' >&2
      printf '  again once it has.\n' >&2
    fi
    exit 24
  fi
}

# adopt_standing_ruling: a ruling from an earlier handoff still stands.
# The gate may have passed on a ruling stored by an earlier handoff. The
# OVERRULED block renders from $OVERRIDE_ID, so adopt the stored ruling here
# or the builder would get no ruling at all.
adopt_standing_ruling() {
  if [[ -z "$OVERRIDE_ID" ]]; then
    _pass_id=$(printf '%s' "$_gate" | sed -n '1p')
    _pass_meta="$MAILBOX/reviews/$_pass_id.json"
    # Same id shape check as the flag path.
    if [[ "$_pass_id" =~ ^[0-9]{8}-[0-9]{6}-[0-9]+-[0-9]+$ && -f "$_pass_meta" ]]; then
      # Field 2 is the ruling, empty when there is none. A ruling never stands
      # on a clean review, which older sidecars can pair with one.
      tandem gate ruling --relay "$MAILBOX" --id "$_pass_id" >"$FIELDS" 2>/dev/null || true
      { read_field _; read_field _stored; } <"$FIELDS"
      if [[ -n "$_stored" ]]; then
        OVERRIDE_ID="$_pass_id"
        OVERRIDE_REASON="$_stored"
      fi
    fi
  fi
}

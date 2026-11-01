# bin/lib/handback-task.sh: hand back one task of a build, durable or legacy.
#
# Sourced by bin/tandem-handback, never run. The functions read and set its globals.

# bin/tandem-handback sets and reads the globals, which shellcheck cannot see from
# this file alone.
# shellcheck shell=bash disable=SC2034,SC2153,SC2154

# --- a durable run? ---
# Look for the run marker before any legacy build.json, in both the git
# toplevel's relay and the builder's own directory. An unreadable marker stops
# here rather than reading as "no durable run".
detect_durable() {
  local top cand dir found id
  top=$(git rev-parse --show-toplevel 2>/dev/null || true); top="${top:-$PWD}"
  for cand in "$top" "$PWD"; do
    dir=$(tandem paths relay --root "$cand") || die "the installed tandem is behind these commands (no 'tandem paths'); re-run install.sh."
    [[ -e "$dir/active-run.json" ]] || continue
    id=$(tandem state active-run --relay "$dir" 2>/dev/null) && [[ -n "$id" ]] \
      || die "$dir/active-run.json exists but is not a readable durable-run marker. Nothing was sent."
    if [[ -n "$DURABLE_RUN" && "$DURABLE_RUN" != "$id" ]]; then
      die "two durable runs are marked ($DURABLE_RUN and $id); refusing to guess which one this handback is for."
    fi
    DURABLE_RUN="$id"; DURABLE_ROOT="$cand"
  done
}

# durable_handback: a durable run takes the task as an event, then exits.
durable_handback() {
  DURABLE_RUN=""; DURABLE_ROOT=""
  if [[ -n "$TASK" || -n "$RUN_ID" || -n "$ATTEMPT_ID" || -n "$JOB_ID" || -n "$EFFECT_ID" || -n "$NONCE" ]]; then
    detect_durable
  fi
  if [[ -z "$DURABLE_RUN" && -n "$RUN_ID$ATTEMPT_ID$JOB_ID$EFFECT_ID$NONCE" ]]; then
    die "--run/--attempt/--job/--effect/--nonce name a durable run, but none is active here. Nothing was sent."
  fi
  if [[ -n "$DURABLE_RUN" && -n "$TASK" ]]; then
    [[ -n "$RUN_ID" ]] || die "this relay has a durable run ($DURABLE_RUN). Hand back with --run $DURABLE_RUN --attempt ID --job ID --effect ID --nonce ID, as your task prompt says; an old-style handback would be dropped."
    [[ "$RUN_ID" == "$DURABLE_RUN" ]] || die "--run $RUN_ID is not the active run ($DURABLE_RUN). Nothing was sent."
    [[ -n "$ATTEMPT_ID" && -n "$JOB_ID" && -n "$EFFECT_ID" && -n "$NONCE" ]] || die "a durable handback needs --attempt, --job, --effect and --nonce from the task prompt."
    (( REVIEW )) && die "review hand backs are not supported for durable runs yet."
    KIND=builder_submitted
    (( STUCK ))   && KIND=builder_stuck
    (( STARTED )) && KIND=builder_started
    PAYLOAD=""
    if (( REVIEW_STARTED )); then
      KIND=review_started
    elif [[ -n "$REVIEW_RESULT" ]]; then
      KIND=review_result
      [[ -f "$REVIEW_RESULT" ]] || die "review result file not found: $REVIEW_RESULT"
      [[ -s "$REVIEW_RESULT" ]] || die "the review result is empty; nothing to hand back."
      PAYLOAD="$REVIEW_RESULT"
    elif (( ! STARTED )); then
      if [[ -n "$REPORT_FILE" && "$REPORT_FILE" != "-" ]]; then
        [[ -f "$REPORT_FILE" ]] || die "report file not found: $REPORT_FILE"
        PAYLOAD="$REPORT_FILE"
      else
        [[ -t 0 ]] && die "no report given. Pass --report FILE, or pipe the report in on stdin."
        DRELAY=$(tandem paths relay --root "$DURABLE_ROOT")
        PAYLOAD="$DRELAY/.payload.$$"
        cat > "$PAYLOAD"
        trap 'rm -f "$PAYLOAD"' EXIT
      fi
      [[ -s "$PAYLOAD" && -n "$(tr -d '[:space:]' < "$PAYLOAD" | head -c1)" ]] || die "the report is empty; nothing to hand back."
    fi
    if (( DRY_RUN )); then
      printf 'tandem-handback: durable run %s, %s for task %s\n' "$DURABLE_RUN" "$KIND" "$TASK"
      printf '  [dry-run] tandem build event new --kind %s --task %s\n' "$KIND" "$TASK"
      exit 0
    fi
    EARGS=(--root "$DURABLE_ROOT" --kind "$KIND" --task "$TASK" --attempt "$ATTEMPT_ID" --job "$JOB_ID" --effect "$EFFECT_ID" --nonce "$NONCE")
    [[ -n "$PAYLOAD" ]] && EARGS+=(--payload "$PAYLOAD")
    tandem build event new "${EARGS[@]}" >/dev/null || die "could not queue the durable event."
    info "$KIND queued for task $TASK of run $DURABLE_RUN."
    case "$KIND" in
      review_started) ;;
      review_result) info "tandem applies the review once your turn has ended. Stop here." ;;
      *) (( STARTED )) || info "tandem checks it and sends what comes next. Stop here." ;;
    esac
    exit 0
  fi
}

# --- one task of a build ---
# Always through the mailbox, so the bridge drives every build the same way.
task_handback() {
  if [[ -n "$TASK" ]]; then
    BUILD_DIR="$MAILBOX/build"
    [[ -f "$BUILD_DIR/build.json" ]] \
      || die "no build is running in $MAILBOX, so there is no task $TASK to hand back. Hand back without --task."
    if (( REVIEW )); then
      # The review must come from the reviewer pane the build recorded.
      # Field 3 of `build roles` is the reviewer. A record tandem cannot read
      # leaves it empty.
      REVIEWER=$(tandem build roles --relay "$MAILBOX" 2>/dev/null \
        | { IFS= read -r -d '' _; IFS= read -r -d '' _; IFS= read -r -d '' r; printf '%s' "${r:-}"; }) || REVIEWER=""
      [[ -n "$REVIEWER" ]] \
        || die "this build records no reviewer pane, and the builder never reviews. NOTHING WAS SENT. $REVIEWER_REMEDY Then hand off again."
      # tandem decides whether this shell is that pane (cmd/tandem/agent_self.go).
      # In a Herdr pane, Herdr's name wins over a claimed role.
      _id=$(tandem agent self --expect "$REVIEWER" --claimed "$SELF" 2>/dev/null) || _id=""
      explain_reviewer_self "a review" "NOTHING WAS SENT." "$REVIEWER" "$_id"
      SELF="$REVIEWER"
      TREPORT="$BUILD_DIR/task-$TASK.review.md"
    else
      TREPORT="$BUILD_DIR/task-$TASK.report.md"
    fi
    if (( DRY_RUN )); then
      printf 'tandem-handback: task %s from %s%s%s\n' "$TASK" "$SELF" "$( (( STUCK )) && printf ' (stuck)')" "$( (( REVIEW )) && printf ' (review: %s)' "$VERDICT")"
      printf '  [dry-run] write report  %s\n' "$TREPORT"
      printf '  [dry-run] queue         %s/q/%s.json for the relay bridge\n' "$MAILBOX" "$ID"
      exit 0
    fi
    mkdir -p "$MAILBOX/q" || die "cannot create the relay directory $MAILBOX/q"
    printf '%s\n' "$REPORT" > "$TREPORT.tmp" && mv "$TREPORT.tmp" "$TREPORT" \
      || die "could not write the report file."
    _qflags=()
    (( STUCK )) && _qflags+=(--stuck)
    (( REVIEW )) && _qflags+=(--review)
    tandem relay write task --mailbox "$MAILBOX" --id "$ID" --from "$SELF" --task "$TASK" \
      --verdict "$VERDICT" --report "$TREPORT" --plan "$PLAN_PATH" ${_qflags[@]+"${_qflags[@]}"} \
      || die "could not queue the handback."
    if (( REVIEW )); then
      info "review of task $TASK handed back ($VERDICT); findings at $TREPORT"
      info "tandem records it and tells the builder what to fix, if anything. Stop here."
    else
      info "task $TASK handed back; report at $TREPORT"
      info "tandem checks it and sends the next task to a fresh conversation. Stop here."
    fi
    exit 0
  fi
}

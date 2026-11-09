# bin/lib/handback-task.sh: hand back one task of the active run.
#
# Sourced by bin/tandem-handback, never run. The functions read and set its globals.

# bin/tandem-handback sets and reads the globals, which shellcheck cannot see from
# this file alone.
# shellcheck shell=bash disable=SC2034,SC2153,SC2154

# --- a run? ---
# Look for the run marker in both the git toplevel's relay and the builder's
# own directory. An unreadable marker stops
# here rather than reading as "no run".
detect_durable() {
  local top cand dir found id
  top=$(git rev-parse --show-toplevel 2>/dev/null || true); top="${top:-$PWD}"
  for cand in "$top" "$PWD"; do
    dir=$(tandem paths relay --root "$cand") || die "the installed tandem is behind these commands (no 'tandem paths'); re-run install.sh."
    [[ -e "$dir/active-run.json" ]] || continue
    id=$(tandem state active-run --relay "$dir" 2>/dev/null) && [[ -n "$id" ]] \
      || die "$dir/active-run.json exists but is not a readable durable-run marker. Nothing was sent."
    if [[ -n "$DURABLE_RUN" && "$DURABLE_RUN" != "$id" ]]; then
      die "two runs are marked ($DURABLE_RUN and $id); refusing to guess which one this handback is for."
    fi
    DURABLE_RUN="$id"; DURABLE_ROOT="$cand"
  done
}

# durable_handback: a run takes the task as an event, then exits.
durable_handback() {
  DURABLE_RUN=""; DURABLE_ROOT=""
  if [[ -n "$TASK" || -n "$RUN_ID" || -n "$ATTEMPT_ID" || -n "$JOB_ID" || -n "$EFFECT_ID" || -n "$NONCE" ]]; then
    detect_durable
  fi
  if [[ -z "$DURABLE_RUN" && -n "$RUN_ID$ATTEMPT_ID$JOB_ID$EFFECT_ID$NONCE" ]]; then
    die "--run/--attempt/--job/--effect/--nonce name a run, but none is active here. Nothing was sent."
  fi
  if [[ -z "$DURABLE_RUN" && -n "$TASK" ]]; then
    die "no run is active here, so there is no task $TASK to hand back. Hand back without --task. Nothing was sent."
  fi
  if [[ -n "$DURABLE_RUN" && -n "$TASK" ]]; then
    [[ -n "$RUN_ID" ]] || die "this relay has a run ($DURABLE_RUN). Hand back with --run $DURABLE_RUN --attempt ID --job ID --effect ID --nonce ID, as your task prompt says; an old-style handback would be dropped."
    [[ "$RUN_ID" == "$DURABLE_RUN" ]] || die "--run $RUN_ID is not the active run ($DURABLE_RUN). Nothing was sent."
    [[ -n "$ATTEMPT_ID" && -n "$JOB_ID" && -n "$EFFECT_ID" && -n "$NONCE" ]] || die "a durable handback needs --attempt, --job, --effect and --nonce from the task prompt."
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
      printf 'tandem-handback: run %s, %s for task %s\n' "$DURABLE_RUN" "$KIND" "$TASK"
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

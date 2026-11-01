# bin/lib/handoff-start.sh: start or continue the build and choose what to send.
#
# Sourced by bin/tandem-handoff, never run. The functions read and set its
# globals; tandem makes the decisions and returns NUL fields in $FIELDS.

# bin/tandem-handoff sets and reads the globals, which shellcheck cannot see
# from this file alone.
# shellcheck shell=bash disable=SC2034,SC2153,SC2154

# check_planner: warn when no planner pane can take questions and handbacks.
check_planner() {
  rc=0
  _snap=$(tandem agent snapshot "$PLANNER_NAME" 2>/dev/null) || rc=$?
  if (( rc == 0 )); then
    IFS=$'\t' read -r PLANNER_PANE _ _ <<<"$_snap"
  else
    PLANNER_PANE=""
  fi
  if [[ -z "$PLANNER_PANE" ]]; then
    printf 'tandem-handoff: WARNING no agent named %s.\n' "$PLANNER_NAME" >&2
    printf '               The builder is told to use `tandem-ask %s` and `tandem-handback`.\n' "$PLANNER_NAME" >&2
    printf '               Both will fail, so its questions and finished work have nowhere to go.\n' >&2
    printf '               Run tandem-pair first.\n' >&2
  fi
}

# start_build: the task to send.
# Each handoff sends one task brief to a fresh conversation, and the relay
# bridge sends the next after handback. A stopped build resumes at its first
# unfinished task.
start_build() {
  BUILDER_KIND=""
  if tandem config builder --relay "$MAILBOX" >"$FIELDS" 2>/dev/null; then
    read_field BUILDER_KIND <"$FIELDS"
  fi
  BARGS=( --plan "$PLAN_ABS" --root "$ROOT" --builder "$BUILDER_NAME" --planner "$PLANNER_NAME"
          --check "$CHECK_COMMAND" --budget "${TASK_BUDGET:-0}" )
  [[ "${HERDR_JUDGE:-0}" == 1 && -n "${TYPESAFE_API_KEY:-}" ]] && BARGS+=( --judge )
  (( CONTINUE )) && BARGS+=( --resume-only )
  (( DRY_RUN )) && BARGS+=( --dry-run )
  # A durable run needs the builder's kind to choose its transport. The legacy
  # engine does not accept the flag. The variable overrides build_engine.
  DURABLE_START=0
  if [[ "${TANDEM_BUILD_ENGINE:-$PROJ_BUILD_ENGINE}" == durable || -f "$MAILBOX/active-run.json" ]]; then
    DURABLE_START=1
    BARGS+=( --builder-kind "$BUILDER_KIND" )
    [[ -n "$CONTEXT_LIMIT" ]] && BARGS+=( --context-limit "$CONTEXT_LIMIT" )
    # Only new runs read these settings. A persisted run keeps its own.
    if [[ ! -f "$MAILBOX/active-run.json" ]]; then
      [[ -n "$REVIEW_DELIVERY" ]] && BARGS+=( --review-delivery "$REVIEW_DELIVERY" )
      if [[ -n "$ACCEPTANCE_POLICY" ]]; then
        [[ "$ACCEPTANCE_POLICY" == /* ]] || ACCEPTANCE_POLICY="$ROOT/$ACCEPTANCE_POLICY"
        BARGS+=( --acceptance-policy "$ACCEPTANCE_POLICY" )
      fi
      [[ -n "$COLLABORATION" ]] && BARGS+=( --collaboration "$COLLABORATION" )
    fi
  fi
  # Both engines refuse with 23 when no reviewer pane is recorded.
  rc=0
  tandem build start "${BARGS[@]}" --fields >"$FIELDS" || rc=$?
  # The fields are printed on a refusal too, so read them whatever the exit code.
  { read_field B_ENGINE; read_field RUN_ID; read_field B_DRY_RUN; read_field TASK; read_field TOTAL
    read_field TASK_TITLE; read_field BRIEF; read_field LEDGER_TEXT; read_field RESUMED; read_field REPLACED
    read_field B_JUDGE; read_field REVIEWER_TARGET; read_field REVIEW_PENDING; read_field REVIEW_PROMPT_FILE; } <"$FIELDS"
  if (( rc != 0 )); then
    if (( CONTINUE && rc == 23 )); then
      printf 'tandem-handoff: the durable run is paused and needs a person (for example the plan file changed); nothing was sent. See `tandem build status --root %s`.\n' "$ROOT" >&2
      exit "$rc"
    fi
    if (( rc == 23 )) && [[ -z "$RUN_ID" ]]; then
      # Refused before anything was created. The start printed why.
      if (( DURABLE_START )); then
        printf 'tandem-handoff: no durable run was started; the reason is above. Nothing was sent. A task too large for the context limit is raised with --context-limit N, or context_limit in .tandem.json.\n' >&2
      else
        printf 'tandem-handoff: the build was not started; the reason is above. Nothing was sent.\n' >&2
      fi
      exit "$rc"
    fi
    if (( rc == 23 )); then
      printf 'tandem-handoff: the durable run was created but is paused and needs a person; nothing was sent. See `tandem build status --root %s`.\n' "$ROOT" >&2
      exit "$rc"
    fi
    (( CONTINUE )) && { printf 'tandem-handoff: nothing to continue; the plan was NOT delivered.\n' >&2; exit "$rc"; }
    die "could not start the build (rc $rc); the reason is above. Only if it says 'tandem build' is unknown, re-run install.sh."
  fi
  # A durable dry run creates nothing, so it has no run id.
  if (( DRY_RUN )) && [[ "$B_ENGINE" == durable && "$B_DRY_RUN" == true && -z "$RUN_ID" ]]; then
    printf 'tandem-handoff: [dry-run] a durable run would start for %s; nothing was sent.\n' "$BUILDER_NAME"
    exit 0
  fi
}

# A durable run delivers its own prompts, so handoff only ticks it once to
# send the first task. The bridge keeps ticking.
follow_durable_run() {
  if [[ -n "$RUN_ID" ]]; then
    if (( DRY_RUN )); then
      printf 'tandem-handoff: [dry-run] durable run %s would continue for %s; nothing was sent.\n' "$RUN_ID" "$BUILDER_NAME"
      exit 0
    fi
    rc=0
    tandem build tick --root "$ROOT" >/dev/null 2>"$MAILBOX/.handoff_tick_error" || rc=$?
    if (( rc != 0 )); then
      cat >&2 <<MSG
tandem-handoff: WARNING durable run $RUN_ID exists but its first step failed (rc $rc): $(tail -1 "$MAILBOX/.handoff_tick_error" 2>/dev/null)

  Nothing was recorded as delivered. The relay bridge keeps ticking the run;
  \`tandem build recover --root $ROOT\` continues it by hand, and
  \`tandem build status --root $ROOT\` shows where it is.
MSG
      exit 7
    fi
    verb=started; (( CONTINUE )) && verb=continued
    # Wait until the first send is confirmed or fails for good, so a builder whose
    # turn ends at once is reported instead of "started". Only poll here: ticking
    # would hold the run's lease and starve the bridge.
    WAIT_S="${TANDEM_DISPATCH_WAIT_S:-120}"
    [[ "$WAIT_S" =~ ^[1-9][0-9]*$ ]] || WAIT_S=120
    rc=0
    _werr=$(mktemp "${TMPDIR:-/tmp}/tandem-handoff-wait.XXXXXX")
    tandem build wait --root "$ROOT" --until sent --until-not running \
      --timeout "$(( WAIT_S * 1000 ))" --fields >"$FIELDS" 2>"$_werr" || rc=$?
    _werrtxt=$(cat "$_werr" 2>/dev/null || true); rm -f "$_werr"
    # Printed at the timeout too: the state, its pause code, the first unfinished
    # task's dispatch, and the pause reason.
    { read_field _state; read_field _code; read_field _disp; read_field _reason; } <"$FIELDS"
    if [[ "$_disp" == sent ]]; then
      printf 'tandem-handoff: durable run %s %s for %s.\n' "$RUN_ID" "$verb" "$BUILDER_NAME"
      exit 0
    fi
    if [[ "$_state" == paused && "$_code" == dispatch_failed ]]; then
      cat >&2 <<MSG
tandem-handoff: WARNING durable run $RUN_ID could not deliver its task to $BUILDER_NAME, so nothing is building the plan.

$(printf '%s\n' "${_reason:-No reason was recorded.}" | sed 's/^/    /')

  A model the CLI rejects, an expired login or used-up quota looks like this.
  Fix it in pane $BUILDER_PANE (or re-run tandem-pair), then:

    tandem build stop --root $ROOT
    tandem-handoff $PLAN_ABS
MSG
      exit 8
    fi
    if [[ "$_state" == paused ]]; then
      printf 'tandem-handoff: durable run %s paused (%s) and needs a person; the task was not confirmed as sent. See `tandem build status --root %s`.\n' "$RUN_ID" "${_code:-no code}" "$ROOT" >&2
      exit 23
    fi
    if [[ -n "$_state" && "$_state" != running ]]; then
      printf 'tandem-handoff: durable run %s is %s.\n' "$RUN_ID" "$_state"
      exit 0
    fi
    if (( rc == 23 )); then
      printf 'tandem-handoff: durable run %s %s for %s, but its task was not confirmed as sent within %ss. The relay bridge keeps trying and tells you if it fails; `tandem build status --root %s` shows where it is.\n' \
        "$RUN_ID" "$verb" "$BUILDER_NAME" "$WAIT_S" "$ROOT"
      exit 0
    fi
    if (( rc != 0 )); then
      printf 'tandem-handoff: could not read durable run %s: %s\n' "$RUN_ID" "${_werrtxt:-tandem build wait exited $rc}" >&2
      exit 0
    fi
  fi
}

# check_task: the legacy build named a task to send.
check_task() {
  [[ -n "$REPLACED" ]] && info "WARNING an unfinished build of $REPLACED was replaced; this plan starts again at task 1."
  [[ "$TASK" =~ ^[0-9]+$ && "$TOTAL" =~ ^[0-9]+$ && -n "$BRIEF" ]] \
    || die "the build record gave no task to send. The installed tandem may be behind these commands; re-run install.sh."
}

# A task paused on its review budget already has its implementation. Send the
# stored review prompt to the reviewer pane, with no reset and no `build sent`.
send_pending_review() {
  if [[ "$REVIEW_PENDING" == true ]]; then
    # REVIEWER_TARGET is never empty: the start refused above without a reviewer.
    printf 'tandem-handoff: task    %s of %s: %s is paused at its review; sending the review, not the task.\n' "$TASK" "$TOTAL" "$TASK_TITLE"
    if (( DRY_RUN )); then
      # A dry run writes nothing, so the prompt file does not exist yet.
      printf 'tandem-handoff: [dry-run] the stored review prompt would go to %s; nothing was sent.\n' "$REVIEWER_TARGET"
      exit 0
    fi
    [[ -n "$REVIEW_PROMPT_FILE" && -r "$REVIEW_PROMPT_FILE" ]] \
      || die "task $TASK is paused at its review but its review prompt is missing ($REVIEW_PROMPT_FILE). Re-run install.sh if tandem is out of date."
    REVIEWER_PANE=""; REVIEWER_STATUS=""
    rc=0
    _rsnap=$(tandem agent snapshot "$REVIEWER_TARGET" 2>/dev/null) || rc=$?
    (( rc == 0 )) || die "no agent named '$REVIEWER_TARGET', the reviewer pane this build records. Re-run tandem-pair, then hand off again. Nothing was sent."
    IFS=$'\t' read -r REVIEWER_PANE _ REVIEWER_STATUS <<<"$_rsnap"
    [[ "$REVIEWER_STATUS" == blocked ]] \
      && die "$REVIEWER_TARGET is blocked at an approval or question dialog. Inspect pane $REVIEWER_PANE and clear it first."
    # --as proves the prompt started a turn that keeps running.
    rc=0
    _pres=$(tandem agent prompt "$REVIEWER_TARGET" "$(cat "$REVIEW_PROMPT_FILE")" --as review 2>&1) || rc=$?
    IFS=$'\t' read -r _code _msg <<<"$_pres"
    case "$rc" in
      0)
        tandem build review-sent --root "$ROOT" --task "$TASK" >/dev/null 2>&1 \
          || info "WARNING could not record the review of task $TASK as sent."
        info "review of task $TASK handed to '$REVIEWER_TARGET' (now $_msg)."
        exit 0 ;;
      25)
        cat >&2 <<MSG
tandem-handoff: WARNING the review was accepted but $REVIEWER_TARGET never started working.

  It is most likely sitting at a native dialog that Herdr cannot see. Check
  pane $REVIEWER_PANE, clear any dialog, then re-run this command. The review
  is still recorded as pending, so nothing is sent twice.
MSG
        exit 7 ;;
      26)
        turn_ended_at_once "the review of task $TASK" "The review is still recorded as pending, so a re-run sends it again." reviewer "$REVIEWER_PANE"
        exit 8 ;;
    esac
    die "could not send the review to $REVIEWER_TARGET: ${_msg:-${_code:-unknown error}}. Nothing was recorded as sent."
  fi
}

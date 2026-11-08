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

# check_recorded_kinds: refuse a pair recorded with a kind tandem no longer
# supports, on either engine, before anything is written or typed. A durable
# --continue is left to the run, which its own tick has paused.
check_recorded_kinds() {
  local rc=0 role kind supported
  (( CONTINUE )) && [[ -f "$MAILBOX/active-run.json" ]] && return 0
  tandem config check-kinds --relay "$MAILBOX" >"$FIELDS" 2>/dev/null || rc=$?
  case "$rc" in
    0) return 0 ;;
    20) ;;
    *) die "the installed tandem cannot check the recorded agent kinds (rc $rc); re-run install.sh." ;;
  esac
  { read_field role; read_field kind; read_field supported; } <"$FIELDS"
  printf 'tandem-handoff: this pair records %s as its %s, and tandem has discontinued %s. Nothing was sent.\n' "$kind" "$role" "$kind" >&2
  printf 'tandem-handoff: close that pane and re-pair with a supported kind: tandem-pair --%s KIND (one of %s).\n' "$role" "$supported" >&2
  exit 20
}

# legacy_leftover_note: tandem refused because the removed legacy engine left
# its build record in this relay. %q keeps the printed command pasteable.
legacy_leftover_note() {
  printf 'tandem-handoff: this relay still holds a build from the legacy engine: %s\n' "$LEGACY_BUILD" >&2
  printf '  The legacy engine is gone, so tandem cannot continue that build. Nothing was sent.\n' >&2
  printf '  To finish it, install the last release that has legacy: install.sh --version v0.6.0\n' >&2
  printf '  To start a new run instead, remove the file: rm %q\n' "$LEGACY_BUILD" >&2
}

# start_build: start the run, or continue the one in this relay.
start_build() {
  BUILDER_KIND=""
  if tandem config builder --relay "$MAILBOX" >"$FIELDS" 2>/dev/null; then
    read_field BUILDER_KIND <"$FIELDS"
  fi
  BARGS=( --plan "$PLAN_ABS" --root "$ROOT" --builder "$BUILDER_NAME" --planner "$PLANNER_NAME"
          --check "$CHECK_COMMAND" --budget "${TASK_BUDGET:-0}" )
  (( CONTINUE )) && BARGS+=( --resume-only )
  (( DRY_RUN )) && BARGS+=( --dry-run )
  # The run chooses its transport from the builder's kind.
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
  # A start refuses with 23 when no reviewer pane is recorded.
  rc=0
  tandem build start "${BARGS[@]}" --fields >"$FIELDS" || rc=$?
  # The fields are printed on a refusal too, so read them whatever the exit code.
  { read_field B_ENGINE; read_field RUN_ID; read_field B_DRY_RUN; read_field LEGACY_BUILD; } <"$FIELDS"
  if (( rc != 0 )); then
    if (( rc == 23 )) && [[ -n "$LEGACY_BUILD" ]]; then
      legacy_leftover_note
      exit "$rc"
    fi
    if (( CONTINUE && rc == 23 )); then
      printf 'tandem-handoff: the durable run is paused and needs a person (for example the plan file changed); nothing was sent. See `tandem build status --root %s`.\n' "$ROOT" >&2
      exit "$rc"
    fi
    if (( rc == 23 )) && [[ -z "$RUN_ID" ]]; then
      # Refused before anything was created. The start printed why.
      printf 'tandem-handoff: no durable run was started; the reason is above. Nothing was sent. A task too large for the context limit is raised with --context-limit N, or context_limit in .tandem.json.\n' >&2
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
  [[ -n "$RUN_ID" ]] || die "the start named no run. The installed tandem may be behind these commands; re-run install.sh."
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
  # task's dispatch, the pause reason, the task, the builder and reviewer panes, the plan and the collaboration.
  { read_field _state; read_field _code; read_field _disp; read_field _reason; read_field _task; read_field _builder; read_field _reviewer; read_field _plan; read_field _collab; } <"$FIELDS"
  if [[ "$_disp" == sent ]]; then
    printf 'tandem-handoff: durable run %s %s for %s.\n' "$RUN_ID" "$verb" "$BUILDER_NAME"
    exit 0
  fi
  if [[ "$_state" == paused ]]; then
    if [[ "$_code" == dispatch_failed ]]; then
      printf 'tandem-handoff: WARNING durable run %s could not deliver its task to %s, so nothing is building the plan.\n\n' "$RUN_ID" "$BUILDER_NAME" >&2
      _rc=8
    else
      printf 'tandem-handoff: durable run %s paused (%s) and needs a person; the task was not confirmed as sent.\n\n' "$RUN_ID" "${_code:-no code}" >&2
      _rc=23
    fi
    pause_note "$_code" "$RUN_ID" "$ROOT" "$_task" "$_builder" "$_reviewer" "$_reason" "$_plan" "$_collab" "$MAILBOX" >&2
    exit "$_rc"
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
}

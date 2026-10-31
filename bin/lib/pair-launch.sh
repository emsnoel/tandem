# bin/lib/pair-launch.sh: resolve and launch the builder and the reviewer.
#
# Sourced by bin/tandem-pair, never run. The functions read and set its
# globals; tandem makes the decisions and returns NUL fields in $FIELDS.

# bin/tandem-pair sets and reads the globals, which shellcheck cannot see from
# this file alone.
# shellcheck shell=bash disable=SC2034,SC2153

# mutate <args...>: a state-changing herdr call, skipped under --dry-run.
MUTATE_OUT=""; MUTATE_ERR=""
mutate() {
  if (( DRY_RUN )); then
    printf '  [dry-run] herdr %s\n' "$*"
    MUTATE_OUT=""; MUTATE_ERR=""
    return 0
  fi
  local rc=0
  MUTATE_OUT=$(tandem "$@" 2>&1) || rc=$?
  if (( rc != 0 )); then
    MUTATE_ERR="$MUTATE_OUT"
    MUTATE_OUT=""
  else
    MUTATE_ERR=""
  fi
  return "$rc"
}

# prelaunch_gates <kind> [role]: run and report the pre-launch gates for an
# unattended agent. The registry decides which gates apply. Exit 23 means the
# agent would stall at a dialog.
prelaunch_gates() {
  local kind="$1" role="${2:-builder}" user_kind="${3:-}" pre_rc=0 dry_arg="" show=new g st d r
  PRE_ERR=$(mktemp)
  (( DRY_RUN )) && dry_arg="--dry-run"
  # Doctor already reported the gates that warn, need a human or lack their
  # tool. It runs the gates dry, so only a gate that acted, or failed to write,
  # is new. Without doctor, every gate that is not current is reported.
  (( SKIP_DOCTOR )) && show=all
  tandem agent prelaunch --role "$role" --kind "$kind" \
    --root "$ROOT" --cwd "$SPLIT_CWD" --user-kind "$user_kind" ${dry_arg:+$dry_arg} \
    --fields --show "$show" >"$FIELDS" 2>"$PRE_ERR" || pre_rc=$?

  # Warnings are not fatal, so the stderr block below does not print them.
  while IFS= read -r -d '' g && IFS= read -r -d '' st && IFS= read -r -d '' d && IFS= read -r -d '' r; do
    info "  $g: $st. $d"
    [[ -z "$r" ]] || info "         $r"
  done < "$FIELDS"

  if (( pre_rc != 0 )); then
    [[ -s "$PRE_ERR" ]] && cat "$PRE_ERR" >&2
    if [[ "${HERDR_SKIP_HOOK_CHECK:-}" == 1 ]]; then
      info "pre-launch gate overridden (HERDR_SKIP_HOOK_CHECK=1)."
    else
      exit "$pre_rc"
    fi
  fi
  rm -f "$PRE_ERR"
  return 0
}

# start_in_pane <name> <kind> <pane> [agent args...]: start an agent in a pane
# that was just split. Sets START_RC, START_CODE and START_MSG.
#
# `agent start` needs the pane's shell at its prompt, which takes a moment
# after the split, so retry "not an available shell".
START_RC=0; START_CODE=""; START_MSG=""
start_in_pane() {
  local name="$1" kind="$2" pane="$3" attempt err
  shift 3
  tandem pane wait-shell "$pane" --timeout 10000 >/dev/null 2>&1 || true

  START_RC=1
  for attempt in $(seq 1 2); do
    err=""; START_CODE=""; START_MSG=""; START_RC=0
    err=$(tandem agent start "$name" --kind "$kind" --pane "$pane" \
         --timeout "$START_TIMEOUT_MS" -- "$@" 2>&1 >/dev/null) || START_RC=$?
    IFS=$'\t' read -r START_CODE START_MSG <<<"$err"
    if (( START_RC == 0 )); then
      break
    fi
    case "$START_MSG" in
      *"not an available shell"*)
        (( attempt == 1 )) && info "pane not at its prompt yet; retrying..."
        sleep 1 ;;
      *) break ;;
    esac
  done
  return 0
}

# check_alive NAME ROLE: a CLI can exit seconds after it starts.
# `tandem agent alive` watches it briefly and reports why it died.
check_alive() {
  local name="$1" role="$2" out rc=0
  out=$(tandem agent alive "$name" 2>&1) || rc=$?
  (( rc == 0 )) && return 0
  printf 'tandem-pair: the %s quit right after it started, so the pair is not ready.\n\n' "$role" >&2
  printf '%s\n' "$out" | tail -n +2 | sed 's/^/    /' >&2
  printf '\n  Fix what the pane shows, then re-run tandem-pair.\n' >&2
  exit 1
}

# read_resolve: read `tandem config resolve --fields` from $FIELDS into R_*.
read_resolve() {
  R_STATUS=""; R_REASON=""; R_KIND=""; R_MODEL=""; R_EFFORT=""; R_AUTONOMY=""
  R_KIND_FROM=""; R_MODEL_FROM=""; R_EFFORT_FROM=""; R_AUTONOMY_FROM=""; R_UNENFORCED=""
  { IFS= read -r -d '' R_STATUS; IFS= read -r -d '' R_REASON; IFS= read -r -d '' R_KIND
    IFS= read -r -d '' R_MODEL; IFS= read -r -d '' R_EFFORT; IFS= read -r -d '' R_AUTONOMY
    IFS= read -r -d '' R_KIND_FROM; IFS= read -r -d '' R_MODEL_FROM; IFS= read -r -d '' R_EFFORT_FROM
    IFS= read -r -d '' R_AUTONOMY_FROM; IFS= read -r -d '' R_UNENFORCED; } < "$FIELDS" || true
}

# resolve_launch_args ROLE OUTVAR: fill array OUTVAR with ROLE's CLI args and
# leave the kind in LAUNCH_KIND. A refusal prints tandem's remedy and exits with
# its code. A dry run skips the sandbox probe.
#
# The args are NUL-delimited so an argument may contain a newline. bash 3.2
# has no mapfile.
resolve_launch_args() {
  local role="$1" outvar="$2" a
  local flags=()
  case "$role" in
    builder)
      flags=(${OV_BUILDER_KIND:+--kind "$OV_BUILDER_KIND"}
             ${OV_BUILDER_MODEL:+--model "$OV_BUILDER_MODEL"}
             ${OV_BUILDER_EFFORT:+--effort "$OV_BUILDER_EFFORT"}
             ${OV_BUILDER_AUTONOMY:+--autonomy "$OV_BUILDER_AUTONOMY"}) ;;
    reviewer)
      flags=(${OV_REVIEWER_KIND:+--kind "$OV_REVIEWER_KIND"}
             ${OV_REVIEWER_MODEL:+--model "$OV_REVIEWER_MODEL"}
             ${OV_REVIEWER_EFFORT:+--effort "$OV_REVIEWER_EFFORT"}) ;;
  esac
  (( DRY_RUN )) && flags+=(--dry-run)
  # A refusal's remedy, and any warning, arrive on stderr as sentences.
  tandem agent launch-spec --role "$role" --root "$ROOT" --cwd "$SPLIT_CWD" --fields \
    ${flags[@]+"${flags[@]}"} >"$FIELDS" || exit $?
  LAUNCH_KIND=""
  eval "$outvar=()"
  { IFS= read -r -d '' LAUNCH_KIND
    while IFS= read -r -d '' a; do
      eval "$outvar+=(\"\$a\")"
    done; } < "$FIELDS"
}

# resolve_reviewer: resolve the reviewer and whether it must start.
# The reviewer has its own settings and never inherits the builder's. It is
# resolved before any pane changes, so a bad reviewer stops the pairing early.
# `tandem config resolve` reports none, resolved or invalid, and exits 0.
resolve_reviewer() {
  REVIEWER_PANE=""; REVIEWER_KIND=""; REVIEWER_LAUNCH=0; REVIEWER_ARGS=()
  REVIEWER_MODEL=""; REVIEWER_EFFORT=""; REVIEWER_AUTONOMY=""
  tandem config resolve --role reviewer --root "$ROOT" --cwd "$SPLIT_CWD" --fields \
    ${OV_REVIEWER_KIND:+--kind "$OV_REVIEWER_KIND"} \
    ${OV_REVIEWER_MODEL:+--model "$OV_REVIEWER_MODEL"} \
    ${OV_REVIEWER_EFFORT:+--effort "$OV_REVIEWER_EFFORT"} \
    >"$FIELDS" 2>/dev/null || die "could not resolve the reviewer."
  read_resolve
  case "$R_STATUS" in
    resolved)
      REVIEWER_KIND="$R_KIND"; REVIEWER_MODEL="$R_MODEL"
      REVIEWER_EFFORT="$R_EFFORT"; REVIEWER_AUTONOMY="$R_AUTONOMY"
      REVIEWER_FROM="$R_KIND_FROM $R_MODEL_FROM $R_EFFORT_FROM $R_AUTONOMY_FROM"
      [[ -n "$REVIEWER_KIND" ]] || die "the resolved reviewer carried no kind."
      ;;
    invalid)
      die "${R_REASON:-the reviewer configuration is invalid.}"
      ;;
    none)
      [[ -z "$OV_REVIEWER_MODEL$OV_REVIEWER_EFFORT" ]] \
        || die "--reviewer-model and --reviewer-effort need a reviewer. $REVIEWER_REMEDY"
      info "WARNING no reviewer is configured, so review and build will refuse until one is. $REVIEWER_REMEDY"
      ;;
    *)
      die "could not read the reviewer configuration."
      ;;
  esac

  if [[ -n "$REVIEWER_KIND" ]]; then
    rc=0
    _snap=$(tandem agent snapshot "$REVIEWER_NAME" 2>/dev/null) || rc=$?
    if (( rc == 0 )); then
      IFS=$'\t' read -r REVIEWER_PANE _ _ <<<"$_snap"
    elif (( rc != 10 )); then
      die "could not check for an existing reviewer."
    fi
    if [[ -z "$REVIEWER_PANE" ]]; then
      # Only a reviewer about to start needs a launch spec. Approval (exit 22) and
      # refusals are handled as for the builder.
      REVIEWER_LAUNCH=1
      resolve_launch_args reviewer REVIEWER_ARGS
      if (( DRY_RUN )); then
        read -r _kf _mf _ef _af <<<"$REVIEWER_FROM"
        printf '  [dry-run] would start the reviewer: kind %s (%s), model %s (%s), effort %s (%s), autonomy %s (%s)\n' \
          "${REVIEWER_KIND:-default}" "$_kf" "${REVIEWER_MODEL:-default}" "$_mf" \
          "${REVIEWER_EFFORT:-default}" "$_ef" "${REVIEWER_AUTONOMY:-default}" "$_af"
      fi
    fi
  fi
}

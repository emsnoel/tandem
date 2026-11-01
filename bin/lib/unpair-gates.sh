# bin/lib/unpair-gates.sh: the gates tandem-unpair passes before it closes anything.
#
# Sourced by bin/tandem-unpair, never run. The functions read and set its
# globals; tandem makes the decisions and returns NUL fields in $FIELDS.

# bin/tandem-unpair sets and reads the globals, which shellcheck cannot see
# from this file alone.
# shellcheck shell=bash disable=SC2034,SC2153

# reviewer_gate: a reviewer mid-review loses its review when its pane closes. Checked before
# the project's check, which would be wasted.
reviewer_gate() {
  if [[ -n "$REVIEWER_PANE" ]] && (( ! FORCE )); then
    case "$REVIEWER_STATUS" in
      working) printf '%s\n' "tandem-unpair: reviewer is still working; its review would be lost. Wait for it, or pass --force to close it anyway." >&2; exit 9 ;;
      blocked) printf '%s\n' "tandem-unpair: reviewer is blocked at a dialog. Clear pane $REVIEWER_PANE, or pass --force." >&2; exit 9 ;;
    esac
  fi
}

# read_check_settings: the check command and paths that gate 4 and the check
# use.
read_check_settings() {
  CHECK_COMMAND=""
  CHECK_PATHS=()
  if [[ -n "$BUILDER_PANE" && $FORCE -eq 0 && $SKIP_CHECKS -eq 0 ]]; then
    # The command, then each check path. A failure leaves both empty, so gate 6
    # refuses.
    if tandem config check-command --root "$ROOT" --fields >"$FIELDS" 2>/dev/null; then
      IFS= read -r -d '' CHECK_COMMAND <"$FIELDS" || true
      { IFS= read -r -d '' _; while IFS= read -r -d '' _p; do
          if [[ -n "$_p" ]]; then CHECK_PATHS+=( "$_p" ); fi
        done; } <"$FIELDS" || true
    fi
  fi
}

# pre_gate: gates 1, 2 and 6. The builder pane is passed even when empty: cleanup after a builder that is
# already gone skips the sign-off gates.
pre_gate() {
  GATEARGS=( --builder-pane "$BUILDER_PANE"
             --mailbox "$MAILBOX" --root "$ROOT" --status "$BUILDER_STATUS"
             --snapshot "$SNAP" )
  (( FORCE ))       && GATEARGS+=( --force )
  (( SKIP_CHECKS )) && GATEARGS+=( --skip-checks )
  [[ -n "$CHECK_COMMAND" ]] && GATEARGS+=( --has-check-command )
  for _v in ${VERIFIED[@]+"${VERIFIED[@]}"}; do GATEARGS+=( --verified "$_v" ); done
  # check_paths limits gate 4's tree fingerprint to these pathspecs, so
  # unrelated changes in a monorepo do not read as stale. Empty means the whole
  # tree. The +-expansion avoids bash 3.2's empty-array error.
  for _p in ${CHECK_PATHS[@]+"${CHECK_PATHS[@]}"}; do GATEARGS+=( --check-paths "$_p" ); done

  rc=0
  detail=$(tandem gate teardown --phase pre "${GATEARGS[@]}") || rc=$?
  case "$rc" in
    0) ;;
    9)
      case "$detail" in
        working) printf '%s\n' "tandem-unpair: builder is still working. Wait for its handback, or pass --force to close it anyway." >&2 ;;
        blocked) printf '%s\n' "tandem-unpair: builder is blocked at a dialog. It may be waiting to write. Clear pane $BUILDER_PANE, or pass --force." >&2 ;;
      esac
      exit 9 ;;
    4)
      refuse_never_handed_back ;;
    5)
      refuse_unsigned ;;
    6)
      refuse_no_check ;;
    *) die "teardown pre-check failed (exit $rc)." ;;
  esac
}

# run_check: gate 3, the project's own check passes. The only gate that is evidence rather than an assertion. It runs before
# anything is closed, so a failure leaves the pair as it was.
run_check() {
  local root="$1" cmd="$2" log rc=0
  log=$(mktemp)
  info "running check: $cmd"
  # `eval` is deliberate: check_command is a shell string from .tandem.json, as
  # trusted as the code the builder just edited.
  ( cd "$root" && eval "$cmd" ) >"$log" 2>&1 || rc=$?
  if (( rc == 0 )); then
    info "check passed."
    rm -f "$log"
    return 0
  fi
  printf 'tandem-unpair: check FAILED (exit %s); nothing was closed.\n\n' "$rc" >&2
  tail -25 "$log" | sed 's/^/    /' >&2
  rm -f "$log"
  return "$rc"
}

# check_gate: run the check, or name it under --dry-run.
check_gate() {
  if [[ -n "$BUILDER_PANE" && $FORCE -eq 0 && $SKIP_CHECKS -eq 0 ]]; then
    if (( DRY_RUN )); then
      info "would run check: $CHECK_COMMAND"
    else
      run_check "$ROOT" "$CHECK_COMMAND" || {
        printf '\n  %s is still up and still holds its context. Send it back:\n\n' "$BUILDER_NAME" >&2
        printf '    tandem-ask %s "the check is failing: <what you saw>"\n\n' "$BUILDER_NAME" >&2
        printf '  Or close anyway with --force. Do not sign off a red check.\n' >&2
        exit 7
      }
      CHECK_VALIDATED=1
    fi
  fi
}

# post_gate: gate 4, the green result covers the work being signed off. The
# builder can hand back again or change the tree while the check runs.
# Every handback signed off must predate the check.
post_gate() {
  if [[ -n "$BUILDER_PANE" && $FORCE -eq 0 && ${CHECK_VALIDATED:-0} -eq 1 ]]; then
    # Re-read the builder's status after the check. internal/gate makes no herdr
    # calls, so it is passed in as --status-now.
    NOW_STATUS=$(tandem agent status "$BUILDER_NAME" 2>/dev/null) || NOW_STATUS=""
    [[ -n "$NOW_STATUS" ]] \
      || info "WARNING could not re-read $BUILDER_NAME's status after the check; proceeding on the earlier read."

    POSTARGS=( "${GATEARGS[@]}" )
    [[ -n "$NOW_STATUS" ]] && POSTARGS+=( --status-now "$NOW_STATUS" )

    rc=0
    detail=$(tandem gate teardown --phase post "${POSTARGS[@]}") || rc=$?
    case "$rc" in
      0) ;;
      8)
        refuse_moved ;;
      *) die "teardown post-check failed (exit $rc)." ;;
    esac
    info "check covers the signed-off work: no handback, tree or agent change since it began."
  fi
}

# bin/lib/pair-records.sh: record the pair and start the relay bridge.
#
# Sourced by bin/tandem-pair, never run. The functions read and set its
# globals; tandem makes the decisions and returns NUL fields in $FIELDS.

# bin/tandem-pair sets and reads the globals, which shellcheck cannot see from
# this file alone.
# shellcheck shell=bash disable=SC2034,SC2153

# resolve_bcwd: find the builder pane's own cwd.
# builder.json must be written where its readers look: tandem-handoff uses the
# pane's cwd from `agent snapshot` and tandem-verdict uses its own $PWD. The
# split cwd can differ, for an adopted agent or a planner in a subdirectory.
# BCWD is not walked to the git toplevel, because neither reader walks.
resolve_bcwd() {
  BCWD=""; MB_ROOT=""
  if ! (( DRY_RUN )); then
    rc=0
    _snap=$(tandem agent snapshot "$BUILDER_NAME" 2>/dev/null) || rc=$?
    if (( rc == 0 )); then
      IFS=$'\t' read -r _ BCWD _ <<<"$_snap"
    fi
    BCWD="${BCWD:-${CWD_OVERRIDE:-$PWD}}"
    # MB_ROOT is the git toplevel, used only by the relay bridge.
    MB_ROOT=$(git -C "$BCWD" rev-parse --show-toplevel 2>/dev/null || printf '%s' "$BCWD")
  fi
}

# record_builder: record the builder's identity.
# One record that tandem-verdict and the gate read, so they never resolve the
# values again and disagree. It is resolved with the same override flags as the
# launch spec. For an adopted builder it records what tandem asked for, which
# may differ from how the agent was started (see guide/refusals.md). A dry run resolves
# it too, to show the record a real run would write. tandem refuses an empty
# kind or autonomy, which the gate would refuse every review on.
record_builder() {
  IDENTITY_MAILBOX=""
  if (( ! DRY_RUN )); then
    # A separate name from MAILBOX, because the bridge's mailbox comes from the
    # walked root and can differ.
    IDENTITY_MAILBOX=$(tandem paths relay --root "$BCWD" 2>/dev/null) \
      || die "could not resolve the relay mailbox to record the builder identity."
  fi
  _dry=""; (( DRY_RUN )) && _dry=--dry-run
  _rc=0
  tandem config record-builder ${IDENTITY_MAILBOX:+--relay "$IDENTITY_MAILBOX"} ${_dry:+$_dry} \
    --root "$ROOT" --cwd "$SPLIT_CWD" \
    ${OV_BUILDER_KIND:+--kind "$OV_BUILDER_KIND"} \
    ${OV_BUILDER_MODEL:+--model "$OV_BUILDER_MODEL"} \
    ${OV_BUILDER_EFFORT:+--effort "$OV_BUILDER_EFFORT"} \
    ${OV_BUILDER_AUTONOMY:+--autonomy "$OV_BUILDER_AUTONOMY"} \
    ${ADOPTED_KIND:+--adopted "$ADOPTED_KIND"} \
    >"$FIELDS" || _rc=$?
  case "$_rc" in
    0) ;;
    33) die "the builder resolved with no kind or no autonomy, so the review gate would refuse every review. Check the builder settings in .tandem.json." ;;
    *) die "could not record the builder identity${IDENTITY_MAILBOX:+ in $IDENTITY_MAILBOX}." ;;
  esac
  { IFS= read -r -d '' _kind; IFS= read -r -d '' _autonomy; IFS= read -r -d '' IDENTITY; } < "$FIELDS" || true

  if (( DRY_RUN )); then
    printf '  [dry-run] would record builder identity: %s\n' "$IDENTITY"
  else
    info "recorded builder identity: $_kind/$_autonomy"
  fi
}

# record_reviewer: record the reviewer.
# Written beside builder.json. A pair with no reviewer leaves an earlier record
# alone. model_measured says whether the launch model is known: a reviewer
# started here has it, a running one keeps the earlier record only for the same
# name and kind (config.NextReviewer).
record_reviewer() {
  if [[ -n "$REVIEWER_PANE" ]]; then
    _rrelay="${IDENTITY_MAILBOX:-$(tandem paths relay --root "${BCWD:-$SPLIT_CWD}")}"
    _rargs=(--relay "$_rrelay" --kind "$REVIEWER_KIND" --name "$REVIEWER_NAME" --autonomy "$REVIEWER_AUTONOMY")
    (( REVIEWER_STARTED )) && _rargs+=(--started --model "$REVIEWER_MODEL")
    (( DRY_RUN )) && _rargs+=(--dry-run)
    tandem reviewer record "${_rargs[@]}" >"$FIELDS" \
      || die "could not record the reviewer in $_rrelay/reviewer.json"
    { IFS= read -r -d '' _rmodel; IFS= read -r -d '' REVIEWER_REC; } < "$FIELDS" || true
    if (( ! REVIEWER_STARTED )); then
      # A running reviewer keeps its settings, so warn when the resolved values
      # differ. Effort is not recorded, so a declared effort is always reported.
      if [[ -n "$REVIEWER_MODEL" && "$REVIEWER_MODEL" != "$_rmodel" ]]; then
        info "         declared reviewer model '$REVIEWER_MODEL' is UNENFORCED on a running reviewer${_rmodel:+ (it was started with '$_rmodel')}. Close it and re-pair to apply it."
      fi
      if [[ -n "$REVIEWER_EFFORT" ]]; then
        info "         declared reviewer effort '$REVIEWER_EFFORT' is UNENFORCED on a running reviewer. Close it and re-pair to apply it."
      fi
    fi
    if (( DRY_RUN )); then
      printf '  [dry-run] would record reviewer: %s\n' "$REVIEWER_REC"
    else
      info "recorded reviewer: $REVIEWER_KIND as '$REVIEWER_NAME'${_rmodel:+ on $_rmodel}"
    fi
  fi
}

# start_bridge: start the relay bridge.
# Codex's sandbox denies the Herdr socket, so this unsandboxed watcher
# delivers its questions.
start_bridge() {
  if (( ! DRY_RUN )); then
    MAILBOX=$(tandem paths relay --root "$MB_ROOT") \
      || die "the installed tandem is behind these commands (no 'tandem paths'); re-run install.sh."
    mkdir -p "$MAILBOX/q" "$MAILBOX/a"
    ensure_relay_ignored "$MB_ROOT"

    # Record the builder's cwd in the walked mailbox, so tandem-unpair can find the
    # identity mailbox after the builder pane is gone. It stores the path, not the
    # mailbox, so unpair re-resolves it before its rm -rf guard.
    printf '%s\n' "$BCWD" > "$MAILBOX/builder-cwd.tmp" \
      && mv "$MAILBOX/builder-cwd.tmp" "$MAILBOX/builder-cwd" \
      || info "WARNING could not record the builder cwd in $MAILBOX/builder-cwd; tandem-unpair may leave the identity mailbox behind."

    BRIDGE_PID_FILE="$MAILBOX/bridge.pid"
    if [[ -f "$BRIDGE_PID_FILE" ]] && kill -0 "$(cat "$BRIDGE_PID_FILE" 2>/dev/null)" 2>/dev/null; then
      info "relay bridge already running (pid $(cat "$BRIDGE_PID_FILE"))."
    else
      rm -f "$MAILBOX/bridge.ready"
      nohup tandem-bridge --mailbox "$MAILBOX" --planner "$PLANNER_NAME" \
        >>"$MAILBOX/bridge.log" 2>&1 &
      echo $! > "$BRIDGE_PID_FILE"
      for _ in $(seq 1 50); do [[ -f "$MAILBOX/bridge.ready" ]] && break; kill -0 "$(cat "$BRIDGE_PID_FILE")" 2>/dev/null || break; sleep 0.1; done
      if kill -0 "$(cat "$BRIDGE_PID_FILE")" 2>/dev/null; then
        info "relay bridge started (pid $(cat "$BRIDGE_PID_FILE")) watching $MAILBOX/q"
      else
        info "WARNING relay bridge failed to start; see $MAILBOX/bridge.log"
      fi
    fi
  fi
}

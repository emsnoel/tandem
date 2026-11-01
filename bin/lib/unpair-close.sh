# bin/lib/unpair-close.sh: archive, close and remove what the pair left.
#
# Sourced by bin/tandem-unpair, never run. The functions read and set its
# globals; tandem makes the decisions and returns NUL fields in $FIELDS.

# bin/tandem-unpair sets and reads the globals, which shellcheck cannot see
# from this file alone.
# shellcheck shell=bash disable=SC2034,SC2153

# remove_mailbox <dir> <root>: rm -rf a mailbox only when tandem resolves the
# same full path for <root>, since <dir> comes from a cwd read over a socket.
remove_mailbox() {
  local dir="$1" root="$2"
  if [[ ! -d "$dir" ]]; then
    return 0
  fi
  if [[ "$dir" == "$(tandem paths relay --root "$root")" ]]; then
    rm -rf "$dir"
    info "removed $dir."
  else
    info "WARNING $dir is not the mailbox for $root, so it was not removed."
  fi
}

# clean_trust_kind KIND: remove the trust entry tandem added for KIND, unless
# another agent of that kind still runs in the repo.
clean_trust_kind() {
  local _k="$1" others _ex=()
  # Only another running agent of the same kind needs the entry. The builder
  # and reviewer are left out because this unpair closes them.
  [[ -n "$BUILDER_PANE" ]] && _ex+=( --exclude-pane "$BUILDER_PANE" )
  [[ -n "$REVIEWER_PANE" ]] && _ex+=( --exclude-pane "$REVIEWER_PANE" )
  others=$(tandem agent count --kind "$_k" --under "$ROOT" ${_ex[@]+"${_ex[@]}"} 2>/dev/null) || others=""

  # Empty means the query failed. Fail closed before a destructive action.
  if [[ -z "$others" ]]; then
    info "skipping --clean-trust for $_k: could not determine whether other $_k agents are active."
  elif (( others > 0 )); then
    info "skipping --clean-trust for $_k: $others other $_k agent(s) still working in $ROOT."
  elif (( DRY_RUN )); then
    info "would remove any $_k trust entry tandem added for $ROOT."
  else
    if [[ "$(tandem trust clean --kind "$_k" --cwd "$ROOT" 2>/dev/null)" == removed ]]; then
      info "removed the $_k trust entry tandem added for $ROOT."
    else
      info "no tandem $_k trust entry to remove (entries you wrote are never touched)."
    fi
  fi
}

# clean_trust: remove the trust entries tandem added for the builder's and the
# reviewer's kinds. A dry run reports it without touching the trust store.
clean_trust() {
  # Use the kind read from the running pane. A --builder flag is not persisted,
  # so the configured kind may be wrong.
  BUILDER_KIND=""
  if [[ -n "$BUILDER_PANE" ]]; then
    BUILDER_KIND=$(tandem agent kind --pane "$BUILDER_PANE" 2>/dev/null) || BUILDER_KIND=""
  fi
  # Fall back to the configured kind (field 3) when the pane is gone.
  if [[ -z "$BUILDER_KIND" ]] \
     && tandem config resolve --role builder --root "$ROOT" --fields >"$FIELDS" 2>/dev/null; then
    { IFS= read -r -d '' _; IFS= read -r -d '' _; IFS= read -r -d '' BUILDER_KIND; } <"$FIELDS" || true
  fi

  # The reviewer's trust entry is cleaned the same way.
  REVIEWER_KIND=""
  if [[ -n "$REVIEWER_PANE" ]]; then
    REVIEWER_KIND=$(tandem agent kind --pane "$REVIEWER_PANE" 2>/dev/null) || REVIEWER_KIND=""
  fi

  if [[ -z "$BUILDER_KIND" ]]; then
    info "skipping --clean-trust: could not determine the builder kind."
  else
    clean_trust_kind "$BUILDER_KIND"
    if [[ -n "$REVIEWER_KIND" && "$REVIEWER_KIND" != "$BUILDER_KIND" ]]; then
      clean_trust_kind "$REVIEWER_KIND"
    fi
  fi
}

# print_dry_run: what a real run would close and remove.
print_dry_run() {
  printf '\n'
  [[ -n "$REVIEWER_PANE" ]] && printf '  [dry-run] herdr pane close %s\n' "$REVIEWER_PANE"
  [[ -n "$BUILDER_PANE" ]] && printf '  [dry-run] herdr pane close %s\n' "$BUILDER_PANE"
  [[ -n "$BRIDGE_PID"   ]] && printf '  [dry-run] kill %s\n' "$BRIDGE_PID"
  if [[ -f "$MAILBOX/active-run.json" ]]; then
    printf '  [dry-run] tandem build stop --root %s   (then wait for the durable run to settle)\n' "$MB_ROOT"
    _run=$(tandem state active-run --relay "$MAILBOX" 2>/dev/null) || _run=""
    printf '  [dry-run] tandem state archive --run %s --root %s\n' "${_run:-?}" "$MB_ROOT"
  fi
  if (( KEEP_RELAY )); then
    if [[ -d "$MAILBOX" ]]; then
      printf '  [dry-run] keeping %s\n' "$MAILBOX"
    fi
    if [[ -n "$ID_MAILBOX" && -d "$ID_MAILBOX" ]]; then
      printf '  [dry-run] keeping %s\n' "$ID_MAILBOX"
    fi
  else
    if [[ -f "$MAILBOX/build/build.json" ]]; then
      printf '  [dry-run] tandem build archive --root %s\n' "$MB_ROOT"
    fi
    if [[ -d "$MAILBOX" ]]; then
      printf '  [dry-run] rm -rf %s\n' "$MAILBOX"
    fi
    if [[ -n "$ID_MAILBOX" && -d "$ID_MAILBOX" ]]; then
      printf '  [dry-run] rm -rf %s\n' "$ID_MAILBOX"
    fi
  fi
}

# Record sign-off before anything is destroyed. Reached only when every gate
# passed.
record_signoff() {
  for _id in ${VERIFIED+"${VERIFIED[@]}"}; do
    if [[ -f "$MAILBOX/reports/$_id.md" && ! -f "$MAILBOX/reports/$_id.verified" ]]; then
      printf 'verified by %s at %s\n' "$PLANNER_NAME" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
        > "$MAILBOX/reports/$_id.verified"
    fi
  done
}

# A durable run keeps its history in the state home. Stop it, confirm it is
# settled and archive it before the pane closes, so no lease or delivery is
# left open. Only the panes, the bridge and the mailbox are removed.
settle_durable_run() {
  DURABLE_RUN=""
  if [[ -f "$MAILBOX/active-run.json" ]]; then
    DURABLE_RUN=$(tandem state active-run --relay "$MAILBOX" 2>/dev/null) || DURABLE_RUN=""
    [[ -n "$DURABLE_RUN" ]] \
      || die "$MAILBOX/active-run.json is not a readable durable-run marker, so nothing was closed or deleted."
    _tries="${TANDEM_UNPAIR_SETTLE_POLLS:-10}"
    _state=""
    if tandem build stop --root "$MB_ROOT" >/dev/null 2>&1; then
      # The state is printed on a timeout too, so read it whatever the exit code.
      tandem build wait --root "$MB_ROOT" --until finished --until paused --until archived \
        --timeout "$(( _tries * 1000 ))" --tick --fields >"$FIELDS" 2>/dev/null || true
      IFS= read -r -d '' _state <"$FIELDS" || true
    fi
    case "$_state" in
      finished|paused|archived) ;;
      *)
        if (( FORCE )); then
          info "WARNING durable run $DURABLE_RUN is ${_state:-in an unknown state}; closing anyway. Its state is kept: tandem state recover --run $DURABLE_RUN."
        else
          die "durable run $DURABLE_RUN is ${_state:-in an unknown state}, not settled, so nothing was closed or deleted. Wait for its builder to go idle and run this again, or pass --force."
        fi ;;
    esac
    if [[ "$_state" == finished || "$_state" == paused ]]; then
      if _arch=$(tandem state archive --run "$DURABLE_RUN" --root "$MB_ROOT" 2>&1); then
        info "archived durable run $DURABLE_RUN; its state and artifacts are kept."
      elif (( FORCE )); then
        info "WARNING durable run $DURABLE_RUN was not archived (${_arch:-no output}); closing anyway. Its state is kept: tandem state recover --run $DURABLE_RUN."
      else
        die "durable run $DURABLE_RUN could not be archived, so nothing was closed or deleted: ${_arch:-no output}. A job or delivery is still open; see tandem build status --root $MB_ROOT."
      fi
    fi
  fi
}

# Archive the build record and its evidence first. If the copy cannot be
# verified, stop with the pair intact.
archive_build() {
  if (( ! KEEP_RELAY )) && [[ -f "$MAILBOX/build/build.json" ]]; then
    _arch=$(tandem build archive --root "$MB_ROOT" 2>&1) \
      || die "could not archive the build in $MAILBOX before removing it, so nothing was closed or deleted: ${_arch:-no output}. If 'tandem build archive' is unknown, re-run install.sh."
    if [[ -n "$_arch" ]]; then
      info "$_arch"
    fi
  fi
}

# Close panes before stopping the bridge: if a close fails, the builder still
# has its relay. The reviewer closes first, so a failure leaves a working pair.
close_panes() {
  if [[ -n "$REVIEWER_PANE" ]]; then
    if tandem pane close "$REVIEWER_PANE" >/dev/null; then
      info "closed the reviewer pane $REVIEWER_PANE."
    else
      die "could not close the reviewer pane $REVIEWER_PANE. The builder and the relay are untouched, so the pair still works."
    fi
  fi
  if [[ -n "$BUILDER_PANE" ]]; then
    if tandem pane close "$BUILDER_PANE" >/dev/null; then
      info "closed the builder pane $BUILDER_PANE."
    else
      die "could not close pane $BUILDER_PANE. The relay is untouched, so the pair still works."
    fi
  fi
}

# stop_bridge: stop the relay bridge once both panes are closed.
stop_bridge() {
  if [[ -n "$BRIDGE_PID" ]]; then
    if kill "$BRIDGE_PID" 2>/dev/null; then
      info "stopped the relay bridge (pid $BRIDGE_PID)."
    else
      info "WARNING could not stop the relay bridge (pid $BRIDGE_PID); stop it by hand."
    fi
  fi
}

# remove_mailboxes: remove both mailboxes, or keep them with --keep-relay.
remove_mailboxes() {
  rm -f "$MAILBOX/bridge.ready"
  if (( KEEP_RELAY )); then
    if [[ -d "$MAILBOX" ]]; then
      info "kept $MAILBOX."
    fi
    # With --keep-relay, keep the identity mailbox too, since the reviews live there.
    if [[ -n "$ID_MAILBOX" && -d "$ID_MAILBOX" ]]; then
      info "kept $ID_MAILBOX."
    fi
  else
    remove_mailbox "$MAILBOX" "$MB_ROOT"
    if [[ -n "$ID_MAILBOX" ]]; then
      remove_mailbox "$ID_MAILBOX" "$ID_ROOT"
    fi
  fi
}

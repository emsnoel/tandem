# bin/lib/pair-panes.sh: name, adopt, split and label the panes.
#
# Sourced by bin/tandem-pair, never run. The functions read and set its
# globals; tandem makes the decisions and returns NUL fields in $FIELDS.

# bin/tandem-pair sets and reads the globals, which shellcheck cannot see from
# this file alone.
# shellcheck shell=bash disable=SC2034,SC2153

# ensure_builder: find, adopt or start the builder.
ensure_builder() {
  BUILDER_PANE=""; rc=0
  _snap=$(tandem agent snapshot "$BUILDER_NAME" 2>/dev/null) || rc=$?
  if (( rc == 0 )); then
    IFS=$'\t' read -r BUILDER_PANE _ _ <<<"$_snap"
  elif (( rc != 10 )); then
    die "could not check for an existing builder."
  fi

  if [[ -n "$BUILDER_PANE" ]]; then
    info "builder already present in pane $BUILDER_PANE."
  else
    # Adopt the single unnamed agent in the planner's tab, whatever its kind, so a
    # config change does not start a second one. --builder KIND narrows it. A dry
    # run leaves the planner unnamed, so exclude it by pane id.
    ADOPTED_KIND=""
    tandem agent adoptable --exclude-pane "$PLANNER_PANE" ${PLANNER_TAB:+--tab "$PLANNER_TAB"} \
      ${OV_BUILDER_KIND:+--kind "$OV_BUILDER_KIND"} >"$FIELDS" 2>/dev/null \
      || die "could not list agents to look for one to adopt."
    { IFS= read -r -d '' BUILDER_PANE; IFS= read -r -d '' ADOPTED_KIND; } < "$FIELDS" || true

    if [[ -n "$BUILDER_PANE" ]]; then
      info "adopting the running $ADOPTED_KIND agent in pane $BUILDER_PANE."
      tandem config resolve --role builder --root "$ROOT" --adopted "$ADOPTED_KIND" --fields \
        >"$FIELDS" 2>/dev/null || : >"$FIELDS"
      read_resolve
      _un="$R_UNENFORCED"
      if [[ -n "$_un" ]]; then
        info "         the builder config sets $_un, but the adopted agent keeps the settings it started with."
        info "         Restart its pane to apply them."
      fi
      mutate agent rename "$BUILDER_PANE" "$BUILDER_NAME" \
        || die "could not name the builder: $(printf '%s' "$MUTATE_ERR" | cut -f2-)"
    else
      # Split a wide pane to the right, a narrow or tall pane down.
      if [[ -z "$DIRECTION" ]]; then
        if tandem pane split-direction "$PLANNER_PANE" >"$FIELDS" 2>/dev/null \
          && { IFS= read -r -d '' DIRECTION && IFS= read -r -d '' pw && IFS= read -r -d '' ph; } < "$FIELDS"; then
          info "pane is ${pw}x${ph} cells; splitting $DIRECTION."
        else
          DIRECTION=right
        fi
      fi

      prelaunch_gates "$BUILDER_KIND" builder "$OV_BUILDER_KIND"
      # HERDR_ROLE lets the builder find its own name in mailbox mode, where it
      # cannot reach the socket.
      mutate pane split "$PLANNER_PANE" --direction "$DIRECTION" --cwd "$SPLIT_CWD" \
        --env "HERDR_ROLE=$BUILDER_NAME" --no-focus \
        || die "could not split the planner pane: $(printf '%s' "$MUTATE_ERR" | cut -f2-)"

      if (( DRY_RUN )); then
        # Dry run does not split, so there is no real pane id.
        BUILDER_PANE="<id assigned by the split above>"
        printf '  [dry-run] herdr agent start %s --kind %s --pane %s --timeout %s -- %s\n' \
          "$BUILDER_NAME" "$BUILDER_KIND" "$BUILDER_PANE" "$START_TIMEOUT_MS" "${BUILDER_ARGS[*]}"
      else
        BUILDER_PANE="$MUTATE_OUT"
        [[ -n "$BUILDER_PANE" ]] || die "split succeeded but returned no pane id."
        info "created pane $BUILDER_PANE (cwd: $SPLIT_CWD)."

        start_in_pane "$BUILDER_NAME" "$BUILDER_KIND" "$BUILDER_PANE" ${BUILDER_ARGS+"${BUILDER_ARGS[@]}"}

        if (( START_RC != 0 )); then
          # agent_not_ready means the agent is blocked during startup but its name
          # resolves, so only warn.
          if [[ "$START_CODE" == "agent_not_ready" ]]; then
            info "WARNING: $BUILDER_KIND started but is not ready yet (likely an approval prompt)."
            info "         Check pane $BUILDER_PANE before sending it work."
          else
            die "could not start $BUILDER_KIND: ${START_MSG:-unknown error}"
          fi
        else
          check_alive "$BUILDER_NAME" builder
          info "started $BUILDER_KIND as '$BUILDER_NAME' in pane $BUILDER_PANE."
        fi
      fi
    fi
  fi
}

# ensure_reviewer: find or start the reviewer.
# Review prompts go to this pane, so the builder can run a cheap model and the
# reviewer a strong one. It gets the builder's launch rules with its own kind,
# model and effort, and is never reset.
ensure_reviewer() {
  REVIEWER_STARTED=0
  if [[ -n "$REVIEWER_KIND" ]]; then
    [[ -z "$REVIEWER_PANE" || ( "$REVIEWER_PANE" != "$PLANNER_PANE" && "$REVIEWER_PANE" != "$BUILDER_PANE" ) ]] \
      || die "the agent named '$REVIEWER_NAME' is the planner's or the builder's pane; it cannot review."

    if (( ! REVIEWER_LAUNCH )); then
      # A running reviewer keeps the kind and model it was started with.
      _running=$(tandem agent kind "$REVIEWER_NAME" 2>/dev/null) || _running=""
      info "reviewer already present in pane $REVIEWER_PANE."
      if [[ -n "$_running" && "$_running" != "$REVIEWER_KIND" ]]; then
        info "         it runs $_running, not $REVIEWER_KIND. Close it and re-pair to change it."
        REVIEWER_KIND="$_running"
      fi
      # record_reviewer names a declared model or effort it cannot apply.
    else
      REVIEWER_STARTED=1
      prelaunch_gates "$REVIEWER_KIND" reviewer "$OV_REVIEWER_KIND"
      mutate pane split "$BUILDER_PANE" --direction down --cwd "$SPLIT_CWD" \
        --env "HERDR_ROLE=$REVIEWER_NAME" --no-focus \
        || die "could not split the builder pane for the reviewer: $(printf '%s' "$MUTATE_ERR" | cut -f2-)"
      if (( DRY_RUN )); then
        REVIEWER_PANE="<id assigned by the reviewer split above>"
        printf '  [dry-run] herdr agent start %s --kind %s --pane %s --timeout %s -- %s\n' \
          "$REVIEWER_NAME" "$REVIEWER_KIND" "$REVIEWER_PANE" "$START_TIMEOUT_MS" "${REVIEWER_ARGS[*]}"
      else
        REVIEWER_PANE="$MUTATE_OUT"
        [[ -n "$REVIEWER_PANE" ]] || die "split succeeded but returned no pane id for the reviewer."
        info "created pane $REVIEWER_PANE for the reviewer."
        start_in_pane "$REVIEWER_NAME" "$REVIEWER_KIND" "$REVIEWER_PANE" ${REVIEWER_ARGS+"${REVIEWER_ARGS[@]}"}
        if (( START_RC != 0 )); then
          if [[ "$START_CODE" == "agent_not_ready" ]]; then
            info "WARNING: $REVIEWER_KIND started but is not ready yet (likely an approval prompt)."
            info "         Check pane $REVIEWER_PANE before a review is sent to it."
          else
            die "could not start the $REVIEWER_KIND reviewer: ${START_MSG:-unknown error}"
          fi
        else
          check_alive "$REVIEWER_NAME" reviewer
          info "started $REVIEWER_KIND as '$REVIEWER_NAME' in pane $REVIEWER_PANE."
        fi
      fi
    fi
  fi
}

# label_panes: role tokens for the sidebar, pane and tab labels.
label_panes() {
  mutate pane report-metadata "$PLANNER_PANE" --source tandem-pair --token role=PLAN  || true
  mutate pane report-metadata "$BUILDER_PANE" --source tandem-pair --token role=BUILD || true
  if [[ -n "$REVIEWER_PANE" ]]; then
    mutate pane report-metadata "$REVIEWER_PANE" --source tandem-pair --token role=REVIEW || true
  fi

  # --- pane and tab labels ---
  # Best-effort. The tab is renamed only while it still has Herdr's default
  # number, so a name someone chose is kept.
  mutate pane rename "$PLANNER_PANE" "planner ($PLANNER_KIND)" || true
  mutate pane rename "$BUILDER_PANE" "builder (${ADOPTED_KIND:-$BUILDER_KIND})" || true
  if [[ -n "$REVIEWER_PANE" ]]; then
    mutate pane rename "$REVIEWER_PANE" "reviewer ($REVIEWER_KIND)" || true
  fi
  if [[ -n "$PLANNER_TAB" ]]; then
    if [[ "$(tandem tab label "$PLANNER_TAB" 2>/dev/null)" == default ]]; then
      mutate tab rename "$PLANNER_TAB" "tandem: $(basename "$ROOT")" || true
    fi
  fi
}

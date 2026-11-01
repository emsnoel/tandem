# shellcheck shell=bash
# bin/lib/bridge-build.sh: sourced by bin/tandem-bridge. It carries a legacy
# build from one task to the next.

# tell_planner LINE: send the planner the message a build's last line names,
# `finished ID MSGFILE` or `stopped REASON ID MSGFILE`.
tell_planner() {
  local verb a b c
  read -r verb a b c <<<"$1"
  case "$verb" in
    finished) send_planner "$a" handback builder "$(cat "$b" 2>/dev/null)" ;;
    stopped)  send_planner "$b" handback builder "$(cat "$c" 2>/dev/null)" ;;
  esac
}

# stop_build TASK WHAT DETAIL [--task N]: record that task TASK's next step
# failed, stopping the build, then tell the planner.
stop_build() {
  local task="$1" what="$2" detail="$3" stop rc=0
  shift 3
  stop=$(tandem build stop --root "$(dirname "$MAILBOX")" "$@" --reason handoff \
           --detail "$detail" 2>>"$MAILBOX/.last_error") || rc=$?
  (( rc == 0 )) || { log "task $task: could not record the stop"; return; }
  log "task $task $what; build stopped"
  tell_planner "$stop"
}

# advance_build QFILE: the builder handed back a task, or the reviewer handed
# back its verdict on one.
#
# `tandem build advance` waits for the pane to settle, records the task's
# changes and cost, runs the check, and prints one line:
#   review N MSGFILE | fix N MSGFILE | next N | finished ID MSGFILE
#   | stopped REASON ID MSGFILE | ignored WHY | refused WHY
# review prompts the reviewer pane, fix prompts the builder with the findings,
# next runs tandem-handoff --continue. A failed delivery stops the build and
# tells the planner. The planner hears nothing until the build ends.
advance_build() {
  local qfile="$1" id="" created="" task="" report="" stuck="" review="" verdict="" from="" root out rc
  local active plan="" builder="" reviewer="" _
  if active=$(durable_run); then
    quarantine_v1 "$qfile" "$active"
    return
  fi
  read_message "$qfile"
  { read_field id; read_field _; read_field from; read_field _; read_field _; read_field created
    read_field task; read_field report; read_field stuck; read_field review; read_field verdict; } <"$FIELDS"
  if [[ ! "$task" =~ ^[0-9]+$ ]]; then
    log "malformed task file $qfile, skipped"
    return
  fi
  # The repository root is the mailbox's parent.
  root=$(dirname "$MAILBOX")
  local args=(--root "$root" --task "$task" --event "$id" --created "$created")
  [[ -n "$report" ]] && args+=(--report "$report")
  [[ "$stuck" == true ]] && args+=(--stuck)
  if [[ "$review" == true ]]; then
    args+=(--review --verdict "$verdict" --from "$from")
    log "task $task: review handed back by ${from:-an unnamed pane} ($id, ${verdict:-no verdict})"
  else
    log "task $task handed back ($id); checking it"
  fi
  rc=0
  out=$(tandem build advance "${args[@]}" 2>"$MAILBOX/.last_error") || rc=$?
  if (( rc != 0 )); then
    local err
    err=$(tail -3 "$MAILBOX/.last_error" 2>/dev/null | tr '\n' ' ')
    # A handback for a build that is gone or archived is stale. Nobody waits on
    # it, so the planner is not told.
    if [[ "$err" == *"no build in "* ]]; then
      log "task $task ($id): no build to advance; dropped as stale"
      return
    fi
    log "task $task: tandem build advance FAILED (rc $rc): $err"
    send_planner "$id" handback builder "[build] Tandem could not check task $task that the builder handed back: tandem build advance exited $rc: $err

The build is paused. See \`tandem build status\` and the bridge log at $MAILBOX/bridge.log, then run tandem-handoff to carry on."
    return
  fi
  log "task $task: $out"
  local verb f2 f3 f4
  read -r verb f2 f3 f4 <<<"$out"
  # Who is in the build. A record that cannot be read leaves the plan and
  # reviewer empty and the builder "builder", as the old jq defaults did.
  if tandem build roles --relay "$MAILBOX" >"$FIELDS" 2>/dev/null; then
    { read_field plan; read_field builder; read_field reviewer; } <"$FIELDS"
  fi
  builder=${builder:-builder}
  case "$verb" in
    review)
      local msgfile pres prc=0 detail
      msgfile="$f3"
      # build advance stops a build without a reviewer before it prints review, so
      # one is always named here. --as proves the review started a turn that keeps
      # running. Its result is on stdout or stderr, never both.
      pres=$(tandem agent prompt "$reviewer" "$(cat "$msgfile")" --as review 2>&1) || prc=$?
      if (( prc == 0 )); then
        tandem build review-sent --root "$root" --task "$f2" >/dev/null 2>>"$MAILBOX/.last_error" || true
        log "task $f2: review round sent to $reviewer"
        return
      fi
      printf '%s\n' "$pres" >> "$MAILBOX/.last_error"
      detail="the review prompt for task $f2 was not delivered to $reviewer"
      if (( prc == 26 )); then
        # A turn that ends at once: a rejected model, an expired login or no quota.
        detail="the review prompt for task $f2 reached $reviewer, but its turn ended as soon as it started. Its pane shows:
$(printf '%s\n' "$pres" | tail -n +2)"
      fi
      stop_build "$f2" "review NOT sent" "$detail" --task "$f2"
      ;;
    fix)
      # The reviewer asked for changes. Its findings go to the builder's current
      # conversation, which still holds the task.
      local msgfile pres prc=0 detail
      msgfile="$f3"
      pres=$(tandem agent prompt "$builder" "$(cat "$msgfile")" --as task 2>&1) || prc=$?
      if (( prc == 0 )); then
        log "task $f2: the reviewer's findings sent to $builder to fix"
        return
      fi
      printf '%s\n' "$pres" >> "$MAILBOX/.last_error"
      detail="the reviewer's findings for task $f2 were not delivered to $builder"
      if (( prc == 26 )); then
        detail="the reviewer's findings for task $f2 reached $builder, but its turn ended as soon as it started. Its pane shows:
$(printf '%s\n' "$pres" | tail -n +2)"
      fi
      stop_build "$f2" "fix NOT sent" "$detail" --task "$f2"
      ;;
    next)
      local hout
      rc=0
      hout=$(HERDR_BUILDER_NAME="$builder" HERDR_PLANNER_NAME="$PLANNER_NAME" \
               tandem-handoff "$plan" --continue 2>&1) || rc=$?
      printf '%s\n' "$hout" | sed 's/^/    /'
      if (( rc == 0 )); then
        log "task $f2 sent"
        return
      fi
      local why
      why=$(printf '%s\n' "$hout" | grep -v '^[[:space:]]*$' | tail -2 | tr '\n' ' ')
      stop_build "$f2" "NOT sent (handoff rc $rc)" "tandem-handoff --continue exited $rc: $why"
      ;;
    finished|stopped)
      tell_planner "$out" ;;
    *)
      : ;; # ignored or refused: the log line above says which, and why
  esac
}

# shellcheck shell=bash
# bin/lib/bridge-durable.sh: sourced by bin/tandem-bridge. It drives a durable
# run: it ingests the run's messages, ticks it, and tells the planner when it
# pauses.

# durable_run prints the active durable run. A legacy task message that arrives
# during one is set aside with its reason.
durable_run() {
  [[ -f "$MAILBOX/active-run.json" ]] || return 1
  local id
  id=$(tandem state active-run --relay "$MAILBOX" 2>/dev/null) || return 1
  [[ -n "$id" ]] || return 1
  printf '%s\n' "$id"
}

quarantine_v1() {
  local f="$1" dest
  mkdir -p "$MAILBOX/quarantine-v1"
  dest="$MAILBOX/quarantine-v1/$(basename "$f")"
  mv -n "$f" "$dest" 2>/dev/null || true
  printf 'a legacy (v1) task message arrived while durable run %s is active; a v2 run rejects v1 messages\n' "$2" > "$dest.reason"
  log "set aside $(basename "$f"): a v1 task message cannot drive durable run $2"
}

# ingest_durable TAKEN: apply one claimed protocol-2 message.
# `tandem build event ingest` applies or quarantines it and prints one JSON
# object. A non-zero exit means nothing was decided, so the file stays claimed
# for the next pass. Renaming it back to *.json would wake the loop at once
# into the same failure.
ingest_durable() {
  local taken="$1" root st rc=0 settled=() disp="" why=""
  root=$(dirname "$MAILBOX")
  st=$(tandem agent status "${HERDR_BUILDER_NAME:-builder}" 2>/dev/null || printf unknown)
  case "$st" in idle|done) settled=(--session-settled) ;; esac
  tandem build event ingest --root "$root" --claimed "$taken" ${settled[@]+"${settled[@]}"} --fields \
    >"$FIELDS" 2>"$MAILBOX/.last_error" || rc=$?
  if (( rc != 0 )); then
    log "$(basename "$taken"): not applied yet (rc $rc), kept for the next pass: $(tail -1 "$MAILBOX/.last_error" 2>/dev/null)"
    return
  fi
  { read_field disp; read_field why; } <"$FIELDS"
  log "$(basename "$taken"): $disp $why"
}

# drive_durable VERB [ARGS...]: give the durable run's controller one step (tick
# or recover). A non-zero exit is logged and the loop goes on. Exit 3 is the
# writer lease, which `tandem build recover` resolves.
drive_durable() {
  local verb="$1" root rc=0 run="" state="" code="" skipped=""
  root=$(dirname "$MAILBOX")
  tandem build "$verb" --root "$root" "${@:2}" --fields >"$FIELDS" 2>"$MAILBOX/.last_error" || rc=$?
  if (( rc != 0 )); then
    log "build $verb: exit $rc: $(tail -1 "$MAILBOX/.last_error" 2>/dev/null)"
    return
  fi
  { read_field run; read_field state; read_field code; read_field skipped; } <"$FIELDS"
  if [[ -n "$skipped" ]]; then
    log "build $verb: run $run skipped ($skipped)"
  else
    log "build $verb: run $run $state${code:+ ($code)}"
  fi
  report_pause "$root" "$run" "$state" "$code"
}

# report_pause ROOT RUN STATE CODE: tell the planner once when the durable run
# pauses, since a paused run waits for a person. The run and pause code are
# recorded beside the queue so it is not said twice, and a running run clears
# the mark. The mark is written before sending, so a lost notice is not resent
# every second.
report_pause() {
  local root="$1" run="$2" state="$3" code="$4" mark key reason="" msg _
  [[ -n "$run" && -d "$MAILBOX/durable/$run" ]] || return 0
  mark="$MAILBOX/durable/$run/pause-reported"
  if [[ "$state" != paused ]]; then
    [[ "$state" == running ]] && rm -f "$mark"
    return 0
  fi
  key="$run ${code:-paused}"
  [[ "$(cat "$mark" 2>/dev/null)" == "$key" ]] && return 0
  printf '%s\n' "$key" > "$mark"
  # Field 4 of the status is the pause reason.
  if tandem build status --root "$root" --fields >"$FIELDS" 2>/dev/null; then
    { read_field _; read_field _; read_field _; read_field reason; } <"$FIELDS"
  fi
  msg="[build] Durable run $run paused (${code:-no code}) and waits for a person.${reason:+

$reason}

See \`tandem build status --root $root\`."
  if [[ "$code" == dispatch_failed ]]; then
    msg+="

The task never reached a working builder. A model the CLI rejects, an expired
login or used-up quota looks like this. Fix the builder pane, then restart the
run. A second tandem-handoff is refused while this run is active, so:

1. \`tandem build stop --root $root\`
2. \`tandem state recover --run $run --root $root\` (ends its open jobs)
3. \`tandem state archive --run $run --root $root\`
4. If the pair has no reviewer: $REVIEWER_REMEDY
5. tandem-handoff the plan again."
  fi
  log "run $run paused ($code); telling $PLANNER_NAME"
  send_planner "pause-$run-${code:-paused}" notice tandem "$msg"
}

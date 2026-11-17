# shellcheck shell=bash
# bin/lib/bridge-deliver.sh: sourced by bin/tandem-bridge. It delivers
# questions, handbacks and verdicts to the planner, and reads the script's
# globals.

# Answer a question with an error when delivery fails, so the asker stops
# polling. Handbacks get a marker beside the report instead (see deliver).
fail_answer() {
  local id="$1" reason="$2"
  cat > "$MAILBOX/a/$id.md" <<EOF
[relay error] Your question was not delivered.

Reason: $reason

Do not keep waiting. Either proceed and state the assumption you are making
explicitly in your next message, or stop and ask the human directly.
EOF
}

deliver() {
  local qfile="$1" id="" kind="" from="" question="" msg="" answer_path
  # A file with no kind is a question, and one with no sender is from the
  # builder, named by pair_names in tandem-bridge.
  read_message "$qfile"
  { read_field id; read_field kind; read_field from; read_field question; read_field msg; } <"$FIELDS"
  kind=${kind:-question}
  from=${from:-$BUILDER_NAME}

  # Handbacks and verdicts carry the message their sender composed. The bridge
  # only transports it.
  local need="$question"
  case "$kind" in
    handback|verdict) need="$msg" ;;
    task) need="$id" ;;
  esac
  if [[ -z "$id" || -z "$need" ]]; then
    log "malformed $kind file $qfile, skipped"
    return
  fi
  if [[ "$kind" == task ]]; then
    quarantine_v1 "$qfile"
    return
  fi

  answer_path="$MAILBOX/a/$id.md"
  log "delivering $id from $from (kind: $kind)"
  # A verdict ends the review, so the reviewer's label goes back to plain.
  [[ "$kind" != verdict ]] || activity_sync "$from" ""

  if [[ "$kind" != handback && "$kind" != verdict ]]; then
    msg="[from $from] $question

Reply by writing your answer to this exact path:
  $answer_path

Write the file directly. $from cannot see this conversation, so put
everything it needs in the file. It is polling that path and reads whatever
you write."
  fi

  send_planner "$id" "$kind" "$from" "$msg" || true
}

# send_planner ID KIND FROM MSG [UNMARK]: prompt the planner. A failure
# answers a question with an error, and leaves a marker for anything nobody
# polls. It returns non-zero on failure. Only report_pause reads that; the
# other callers add `|| true`, because the bridge runs under set -e.
#
# A prompt typed into a planner that did not start on it within the confirm
# window is held as pending, not failed: a planner busy in a way Herdr does not
# show reads idle, takes the prompt into its queue and starts on it once that
# work ends. check_pending decides it on later passes, so the loop keeps
# ticking. UNMARK is a file to remove if a pending prompt is given up, so the
# sender sends it again (report_pause's mark).
send_planner() {
  local id="$1" kind="$2" from="$3" msg="$4" unmark="${5:-}" as=question out rc=0 code why seq
  # Each kind's checks are one row in cmd/tandem/deliver.go.
  case "$kind" in handback|verdict|notice) as="$kind" ;; esac
  out=$(tandem agent prompt "$PLANNER_NAME" "$msg" --as "$as" \
          --wait-unblocked "$PROMPT_TIMEOUT_MS" 2>"$MAILBOX/.last_error") || rc=$?
  if (( rc == 0 )); then
    log "$id delivered"
    return 0
  fi
  if seq=$(pending_seq "$out") && pending_hold "$MAILBOX" "$id" "$kind" "$from" "$seq" "$unmark"; then
    log "$id typed, but $PLANNER_NAME has not started on it yet; waiting up to ${PENDING_SECONDS}s for it to"
    return 0
  fi
  code=$(head -1 "$MAILBOX/.last_error" | cut -f1)
  code=${code:-unknown}
  why=$(head -1 "$MAILBOX/.last_error" | cut -s -f2)
  log "$id delivery FAILED ($code)${why:+: $why}"
  mark_undelivered "$id" "$kind" "$from" "$code" "$why"
  return 1
}

# mark_undelivered ID KIND FROM CODE WHY: record a message the planner never got.
mark_undelivered() {
  local id="$1" kind="$2" from="$3" code="$4" why="$5" dir=reports
  if [[ "$kind" == handback || "$kind" == verdict ]]; then
    # Nobody polls for these, so leave a marker beside the report or the stored
    # review. Verdict markers never go in reports/, which tandem-unpair reads for
    # handbacks.
    [[ "$kind" == verdict ]] && dir=reviews
    mkdir -p "$MAILBOX/$dir"
    printf '%s %s from %s was never delivered to %s: tandem agent prompt failed (%s)%s\n' \
      "$kind" "$id" "$from" "$PLANNER_NAME" "$code" "${why:+: $why}" > "$MAILBOX/$dir/$id.UNDELIVERED"
  elif [[ "$kind" != notice ]]; then
    # A notice has nobody waiting on it; the log records the loss.
    fail_answer "$id" "tandem agent prompt failed: $code"
  fi
}

# check_pending: decide each prompt held in MAILBOX/pending (lib/pending.sh).
# One the planner started a turn on since it was typed is delivered. One still
# waiting at its deadline is failed as a send that failed at once would have
# been.
check_pending() {
  local f id kind from seq deadline unmark st
  shopt -s nullglob
  for f in "$MAILBOX"/pending/*; do
    id=${f##*/} kind="" from="" seq="" deadline="" unmark=""
    { IFS= read -r kind; IFS= read -r from; IFS= read -r seq; IFS= read -r deadline; IFS= read -r unmark; } <"$f" || true
    [[ "$seq" =~ ^[0-9]+$ ]] || seq=0
    [[ "$deadline" =~ ^[0-9]+$ ]] || deadline=0
    st=$(tandem agent status "$PLANNER_NAME" --since "$seq" 2>/dev/null || true)
    if [[ "$st" == started$'\t'* ]]; then
      log "$id delivered: $PLANNER_NAME started a turn after it was typed"
      rm -f "$f"
    elif (( $(date +%s) >= 10#$deadline )); then
      log "$id delivery FAILED (agent_never_started): $PLANNER_NAME did not start on it within ${PENDING_SECONDS}s of it being typed"
      mark_undelivered "$id" "${kind:-question}" "${from:-$BUILDER_NAME}" agent_never_started \
        "the prompt was typed, but $PLANNER_NAME did not start on it within ${PENDING_SECONDS}s; it may be at a native dialog Herdr cannot see"
      [[ -n "$unmark" ]] && rm -f "$unmark"
      rm -f "$f"
    fi
  done
  shopt -u nullglob
}

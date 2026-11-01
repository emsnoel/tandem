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
  # builder.
  read_message "$qfile"
  { read_field id; read_field kind; read_field from; read_field question; read_field msg; } <"$FIELDS"
  kind=${kind:-question}
  from=${from:-builder}

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
    advance_build "$qfile"
    # Mark handled whatever the outcome. Startup recovery replays only .taken.
    if [[ -f "$qfile" ]]; then mv -n "$qfile" "${qfile%.taken}.done"; fi
    return
  fi

  answer_path="$MAILBOX/a/$id.md"
  log "delivering $id from $from (kind: $kind)"

  if [[ "$kind" != handback && "$kind" != verdict ]]; then
    msg="[from $from] $question

Reply by writing your answer to this exact path:
  $answer_path

Write the file directly. $from cannot see this conversation, so put
everything it needs in the file. It is polling that path and reads whatever
you write."
  fi

  send_planner "$id" "$kind" "$from" "$msg"
}

# send_planner ID KIND FROM MSG: prompt the planner. A failure answers a
# question with an error, and leaves a marker for anything nobody polls.
send_planner() {
  local id="$1" kind="$2" from="$3" msg="$4" as=question
  # Each kind's checks are one row in cmd/tandem/deliver.go.
  case "$kind" in handback|verdict|notice) as="$kind" ;; esac
  if tandem agent prompt "$PLANNER_NAME" "$msg" --as "$as" \
       --wait-unblocked "$PROMPT_TIMEOUT_MS" >/dev/null 2>"$MAILBOX/.last_error"; then
    log "$id delivered"
  else
    local code why
    code=$(head -1 "$MAILBOX/.last_error" | cut -f1)
    code=${code:-unknown}
    why=$(head -1 "$MAILBOX/.last_error" | cut -s -f2)
    log "$id delivery FAILED ($code)${why:+: $why}"
    if [[ "$kind" == handback || "$kind" == verdict ]]; then
      # Nobody polls for these, so leave a marker beside the report or the stored
      # review. Verdict markers never go in reports/, which tandem-unpair reads for
      # handbacks.
      local dir=reports
      [[ "$kind" == verdict ]] && dir=reviews
      mkdir -p "$MAILBOX/$dir"
      printf '%s %s from %s was never delivered to %s: tandem agent prompt failed (%s)%s\n' \
        "$kind" "$id" "$from" "$PLANNER_NAME" "$code" "${why:+: $why}" > "$MAILBOX/$dir/$id.UNDELIVERED"
    elif [[ "$kind" != notice ]]; then
      # A notice has nobody waiting on it; the log records the loss.
      fail_answer "$id" "tandem agent prompt failed: $code"
    fi
  fi
}

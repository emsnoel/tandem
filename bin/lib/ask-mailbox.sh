# bin/lib/ask-mailbox.sh: the Q&A history and the mailbox path for a sandboxed asker.
#
# Sourced by bin/tandem-ask, never run. The functions read and set its globals.

# bin/tandem-ask sets and reads the globals, which shellcheck cannot see from
# this file alone.
# shellcheck shell=bash disable=SC2034,SC2153,SC2154

# ---------------------------------------------------------------------------
# Durable Q&A history.
#
# Every completed exchange, in either direction and transport, gets one record.
# Best-effort: both call sites use `|| true`, which also disables set -e inside
# the function, so a write failure never fails the exchange.
# ---------------------------------------------------------------------------
log_exchange() {
  local from="$1" to="$2" question="$3" answer="$4"
  local log_dir="$LOG_ROOT/.tandem-log"
  mkdir -p "$log_dir" 2>/dev/null || return 0

  # Local history, never committed.
  if git -C "$LOG_ROOT" rev-parse --git-dir >/dev/null 2>&1; then
    local gi="$LOG_ROOT/.gitignore"
    if [[ ! -f "$gi" ]] || ! grep -qE '^/?\.tandem-log/?$' "$gi" 2>/dev/null; then
      # Grouped so 2>/dev/null also catches a failed redirection.
      { printf '\n# tandem-ask durable Q&A history (local, never committed)\n.tandem-log/\n' >> "$gi"; } 2>/dev/null
    fi
  fi

  # Best effort: a failed write never fails the question.
  tandem relay log --dir "$log_dir" --id "$ID" --from "$from" --to "$to" \
    --question "$question" --answer "$answer" >/dev/null 2>&1 || true
  return 0
}

# ---------------------------------------------------------------------------
# Sandbox fallback.
#
# Codex's sandbox denies the Herdr socket but allows writes in the workspace.
# When the socket is unreachable, the question goes through a mailbox and
# tandem-bridge delivers it from the unsandboxed side.
# ---------------------------------------------------------------------------
mailbox_dir() {
  local root
  root=$(git rev-parse --show-toplevel 2>/dev/null || true)
  # tandem resolves the name, so the bridge and the asker agree on legacy
  # .herdr-relay mailboxes.
  tandem paths relay --root "${root:-$PWD}" \
    || die "the installed tandem is behind these commands (no 'tandem paths'); re-run install.sh."
}

ask_via_mailbox() {
  local mb qfile afile self
  mb=$(mailbox_dir)
  self="${HERDR_ROLE:-builder}"
  [[ "$self" != "$TARGET" ]] || die "refusing to send a question to yourself."

  mkdir -p "$mb/q" "$mb/a" || die "cannot create relay directory $mb"
  qfile="$mb/q/$ID.json"
  afile="$mb/a/$ID.md"

  if (( DRY_RUN )); then
    printf 'tandem-ask: socket unreachable -> mailbox mode\n'
    printf '  [dry-run] write   %s\n' "$qfile"
    printf '  [dry-run] poll    %s (up to %ss)\n' "$afile" "$TIMEOUT_SECONDS"
    exit 0
  fi

  # tandem writes it to a temp name and moves it, so the bridge never reads a
  # partial file.
  tandem relay write question --mailbox "$mb" --id "$ID" --from "$self" --to "$TARGET" \
    --question "$QUESTION" || die "could not write the question file."

  info "socket unreachable (sandbox), so the question went through the mailbox. Waiting up to ${TIMEOUT_SECONDS}s..."

  # The OS reuses pids, so confirm the pid is a tandem-bridge. This runs inside
  # Codex's sandbox, where `ps` is denied but `kill -0` works, so a failed `ps`
  # falls back to trusting kill -0.
  bridge_is_ours() {
    local pid="$1" cmd rc
    [[ -n "$pid" ]] || return 1
    cmd=$(ps -p "$pid" -o command= 2>/dev/null); rc=$?
    (( rc == 0 )) || return 0
    [[ "$cmd" == *tandem-bridge* ]]
  }
  bridge_alive() {
    local pidfile="$mb/bridge.pid" pid
    [[ -f "$pidfile" ]] || return 1
    pid=$(cat "$pidfile" 2>/dev/null || true)
    [[ "$pid" =~ ^[0-9]+$ ]] && kill -0 "$pid" 2>/dev/null && bridge_is_ours "$pid"
  }

  local started=$SECONDS
  local deadline=$(( SECONDS + TIMEOUT_SECONDS ))
  local next_beat=$(( SECONDS + HEARTBEAT_SECONDS ))
  while (( SECONDS < deadline )); do
    if [[ -f "$afile" ]]; then
      printf -- '--- reply from %s ---\n' "$TARGET"
      cat "$afile"
      printf -- '\n--- end reply ---\n'
      log_exchange "$self" "$TARGET" "$QUESTION" "$(cat "$afile" 2>/dev/null || true)" || true
      exit 0
    fi
    # Progress goes to stderr; stdout is only the reply.
    if (( SECONDS >= next_beat )); then
      if bridge_alive; then
        info "still waiting for $TARGET, $(( SECONDS - started ))s of ${TIMEOUT_SECONDS}s. Do not re-run; the question is already queued."
      else
        info "tandem-bridge is not running, so nothing is watching $mb/q and $TARGET cannot see this question until it restarts. tandem-pair restarts it and is safe to re-run. Still waiting, $(( SECONDS - started ))s of ${TIMEOUT_SECONDS}s, in case it comes back."
      fi
      next_beat=$(( SECONDS + HEARTBEAT_SECONDS ))
    fi
    sleep 1
  done

  if bridge_alive; then
    cat >&2 <<MSG
tandem-ask: no answer from $TARGET within ${TIMEOUT_SECONDS}s (mailbox mode).

  Question file: $qfile
  Expected answer at: $afile

  tandem-bridge is running, so the peer itself is likely just busy or slow.
  If the answer was simply slow to arrive, re-ask with a longer --timeout.
  Otherwise do not keep waiting: proceed and state your assumption EXPLICITLY in
  your next message, or stop and ask the human. Do not present a guess as a
  decision. Say that you never got an answer.
MSG
  else
    cat >&2 <<MSG
tandem-ask: no answer from $TARGET within ${TIMEOUT_SECONDS}s (mailbox mode),
and tandem-bridge is not running.

  Question file: $qfile
  Expected answer at: $afile

  This is very likely why no answer arrived: nothing is watching $mb/q.
  Restart the bridge by re-running tandem-pair (idempotent, safe on an
  already-paired session), or check with tandem-status.
  Otherwise do not keep waiting: proceed and state your assumption EXPLICITLY in
  your next message, or stop and ask the human. Do not present a guess as a
  decision. Say that you never got an answer.
MSG
  fi
  exit 5
}

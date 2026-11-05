# shellcheck shell=bash
# bin/lib/bridge-lock.sh: one tandem-bridge per mailbox.
#
# Sourced by bin/tandem-bridge and bin/tandem-pair, never run. Two bridges on one
# relay would each tell the planner about the same pause, so a bridge owns the
# mailbox through MAILBOX/bridge.pid, which only the bridge writes:
#   - it writes its pid there when it starts, and refuses to start when a live
#     tandem-bridge is already named there;
#   - a pid of a bridge that is gone is taken over;
#   - each pass checks the file again. Two bridges that start at the same moment
#     can both take over a stale file, and the one that no longer owns it stops;
#   - it removes the file when it ends by itself. A killed bridge leaves it, and
#     the next bridge takes it over.
# tandem-pair reads the same file to reuse a running bridge.

# bridge_alive PID: PID is a running tandem-bridge. The OS reuses pids, so the
# command line is checked too. When ps cannot answer, kill -0 is trusted.
bridge_alive() {
  local pid="$1" cmd
  [[ "$pid" =~ ^[0-9]+$ ]] && kill -0 "$pid" 2>/dev/null || return 1
  cmd=$(ps -p "$pid" -o command= 2>/dev/null) || return 0
  [[ "$cmd" == *tandem-bridge* ]]
}

# claim_mailbox: make this bridge the mailbox's one bridge, or exit 1 saying
# which bridge already has it. Needs MAILBOX.
claim_mailbox() {
  local file="$MAILBOX/bridge.pid" pid _
  for _ in 1 2 3; do
    # noclobber makes the write fail if the file exists, so one bridge wins.
    if ( set -C; printf '%s\n' "$$" > "$file" ) 2>/dev/null; then
      return 0
    fi
    pid=$(cat "$file" 2>/dev/null || true)
    [[ "$pid" == "$$" ]] && return 0
    if bridge_alive "$pid"; then
      printf 'tandem-bridge: another tandem-bridge (pid %s) already watches %s, so this one is not starting. Stop that one first to replace it.\n' "$pid" "$MAILBOX" >&2
      exit 1
    fi
    rm -f "$file"
  done
  printf 'tandem-bridge: could not claim %s.\n' "$file" >&2
  exit 1
}

# keep_mailbox: at the top of each pass. A missing or stale pid file is taken
# back; a live bridge named there ends this one.
keep_mailbox() {
  local file="$MAILBOX/bridge.pid" pid
  pid=$(cat "$file" 2>/dev/null || true)
  [[ "$pid" == "$$" ]] && return 0
  if bridge_alive "$pid"; then
    log "another tandem-bridge (pid $pid) now watches $MAILBOX, so this one stops."
    exit 0
  fi
  # A mailbox removed under a bridge that is stopping is not recreated here.
  { printf '%s\n' "$$" > "$file"; } 2>/dev/null || true
}

# release_mailbox: on exit, remove the pid file if it is still this bridge's.
release_mailbox() {
  [[ "$(cat "$MAILBOX/bridge.pid" 2>/dev/null)" == "$$" ]] && rm -f "$MAILBOX/bridge.pid"
  return 0
}

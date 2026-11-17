# shellcheck shell=bash
# bin/lib/pending.sh: a prompt typed into the planner that it has not started
# on yet.
#
# Sourced by bin/tandem-bridge, bin/tandem-verdict and bin/tandem-handback. A
# planner busy in a way Herdr does not show reads idle, takes a prompt into its
# queue and starts on it only once that work ends, after `tandem agent prompt
# --as` stopped watching. Such a prompt is held in MAILBOX/pending/ID, and the
# bridge's check_pending (lib/bridge-deliver.sh) decides it on later passes.

# pending_seconds: how long a held prompt waits for the planner to start on it.
pending_seconds() { printf '%s\n' "${HERDR_BRIDGE_PENDING_SECONDS:-1800}"; }

# pending_seq OUT: the pane's lifecycle counter from the stdout of a prompt that
# never started (never_started<TAB>SEQ). Fails when OUT carries none, as from a
# tandem that predates it, since a missing counter would read every later
# change as a start.
pending_seq() {
  local seq
  [[ "$1" == never_started$'\t'* ]] || return 1
  seq=${1#*$'\t'}
  seq=${seq%%$'\n'*}
  [[ "$seq" =~ ^[0-9]+$ ]] || return 1
  printf '%s\n' "$seq"
}

# pending_hold MAILBOX ID KIND FROM SEQ [UNMARK]: hold a prompt, one field per
# line: kind, sender, the pane's counter when it was typed, the epoch second to
# give up at, and a file to remove if it is given up, so its sender sends it
# again.
pending_hold() {
  local mb="$1" id="$2" kind="$3" from="$4" seq="$5" unmark="${6:-}"
  mkdir -p "$mb/pending" || return 1
  printf '%s\n%s\n%s\n%s\n%s\n' "$kind" "$from" "$seq" "$(( $(date +%s) + $(pending_seconds) ))" "$unmark" \
    > "$mb/pending/.$id.tmp" && mv -f "$mb/pending/.$id.tmp" "$mb/pending/$id"
}

# pending_watched MAILBOX: a bridge runs on MAILBOX to decide what is held there.
# Needs bridge_alive (lib/bridge-lock.sh).
pending_watched() {
  bridge_alive "$(cat "$1/bridge.pid" 2>/dev/null || true)"
}

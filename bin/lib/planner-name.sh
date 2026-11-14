# bin/lib/planner-name.sh: the name of the planner a pair delivers to.
#
# Sourced, never run. The builder's and the reviewer's panes never have the
# planner's HERDR_PLANNER_NAME, so tandem-pair records the name in the relay.
# shellcheck shell=bash

# planner_name MAILBOX: HERDR_PLANNER_NAME when set, then the name tandem-pair
# recorded in MAILBOX/planner-name, then "planner". A recorded value that is
# not a Herdr agent name is ignored.
planner_name() {
  local recorded=""
  if [[ -n "${HERDR_PLANNER_NAME:-}" ]]; then
    printf '%s\n' "$HERDR_PLANNER_NAME"
    return
  fi
  [[ -f "$1/planner-name" ]] && IFS= read -r recorded < "$1/planner-name"
  if [[ "$recorded" =~ ^[a-z][a-z0-9_-]{0,31}$ ]]; then
    printf '%s\n' "$recorded"
  else
    printf 'planner\n'
  fi
}

# check_relay_owner MAILBOX: refuse when MAILBOX belongs to another planner's
# live pair. The relay holds one pair's records and one bridge, so a second
# planner would take over the first pair's verdicts and handbacks. A pair is
# live while its bridge runs. Its owner is the planner that bridge delivers to:
# its --planner argument, then the recorded name, then "planner". The owner's
# own pane may pair again under a new name. Reads PLANNER_NAME and
# PLANNER_PANE; needs bridge_alive (lib/bridge-lock.sh).
check_relay_owner() {
  local mb="$1" owner="planner" recorded="" pid cmd snap rc=0 pane=""
  pid=$(cat "$mb/bridge.pid" 2>/dev/null || true)
  bridge_alive "$pid" || return 0
  cmd=$(ps -p "$pid" -o command= 2>/dev/null || true)
  if [[ "$cmd" =~ --planner\ ([a-z][a-z0-9_-]{0,31})(\ |$) ]]; then
    recorded="${BASH_REMATCH[1]}"
  elif [[ -f "$mb/planner-name" ]]; then
    IFS= read -r recorded < "$mb/planner-name"
  fi
  [[ "$recorded" =~ ^[a-z][a-z0-9_-]{0,31}$ ]] && owner="$recorded"
  [[ "$owner" != "$PLANNER_NAME" ]] || return 0
  snap=$(tandem agent snapshot "$owner" 2>/dev/null) || rc=$?
  if (( rc == 0 )); then
    IFS=$'\t' read -r pane _ _ <<<"$snap"
    [[ "$pane" != "$PLANNER_PANE" ]] || return 0
    die "this checkout already has a live pair, run by planner '$owner' in pane $pane. One checkout holds one pair: close that pair with tandem-unpair from pane $pane, or pair this planner in a separate git worktree."
  fi
  die "this checkout's relay bridge still delivers to planner '$owner', which is gone. Clear the old pair with: HERDR_PLANNER_NAME=$owner tandem-unpair --force"
}

# record_planner_name MAILBOX NAME: write the name for planner_name to read.
record_planner_name() {
  printf '%s\n' "$2" > "$1/planner-name.tmp" && mv "$1/planner-name.tmp" "$1/planner-name"
}

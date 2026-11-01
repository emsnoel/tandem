# bin/lib/unpair-find.sh: find the pair, its mailboxes and its bridge, and report them.
#
# Sourced by bin/tandem-unpair, never run. The functions read and set its
# globals; tandem makes the decisions and returns NUL fields in $FIELDS.

# bin/tandem-unpair sets and reads the globals, which shellcheck cannot see
# from this file alone.
# shellcheck shell=bash disable=SC2034,SC2153

# find_builder: the builder's pane, cwd and status. A gone builder leaves them
# empty.
find_builder() {
  BUILDER_PANE=""; BUILDER_CWD=""; BUILDER_STATUS="unknown"; rc=0
  _snap=$(tandem agent snapshot "$BUILDER_NAME" 2>&1) || rc=$?
  case "$rc" in
    0) IFS=$'\t' read -r BUILDER_PANE BUILDER_CWD BUILDER_STATUS <<<"$_snap" ;;
    10) ;;
    *)  die "could not find '$BUILDER_NAME': $(printf '%s' "$_snap" | cut -f2-)" ;;
  esac

  # Never close the pane running this command.
  SELF_PANE="${HERDR_PANE_ID:-}"
  if [[ -n "$BUILDER_PANE" && -n "$SELF_PANE" && "$BUILDER_PANE" == "$SELF_PANE" ]]; then
    die "'$BUILDER_NAME' is this pane. Run tandem-unpair from the $PLANNER_NAME pane."
  fi
}

# resolve_mailboxes: find the mailbox before anything is closed. It comes from the builder's cwd, which is unreadable once the pane is gone.
# $PWD may be a popup's directory.
resolve_mailboxes() {
  ROOT="${BUILDER_CWD:-$PWD}"
  ROOT=$(cd "$ROOT" && pwd -P)
  MB_ROOT=$(git -C "$ROOT" rev-parse --show-toplevel 2>/dev/null || printf '%s' "$ROOT")
  MAILBOX=$(tandem paths relay --root "$MB_ROOT") \
    || die "the installed tandem is behind these commands (no 'tandem paths'); re-run install.sh."
  engine_guard "$MB_ROOT"

  # A pair writes to two mailboxes. $MAILBOX is the relay one at the git
  # toplevel (q/, a/, bridge.pid). builder.json and reviews/ sit at the builder's
  # own cwd, unwalked (see BCWD in bin/tandem-pair). They differ when the builder
  # works in a subdirectory, and a stale review left behind could pass the gate
  # for the next pair. ID_MAILBOX is emptied when both are the same directory.
  #
  # Once the builder pane is gone, $ROOT falls back to $PWD, so prefer the cwd
  # tandem-pair recorded in the relay mailbox.
  ID_ROOT="$ROOT"
  if [[ -z "$BUILDER_CWD" ]]; then
    _ptr=$(cat "$MAILBOX/builder-cwd" 2>/dev/null) || true
    if [[ -n "${_ptr:-}" && -d "$_ptr" ]]; then
      ID_ROOT=$(cd "$_ptr" && pwd -P)
    else
      info "WARNING the builder pane is gone and no builder cwd was recorded; if it"
      info "        was paired from a subdirectory, its identity mailbox may remain."
    fi
  fi
  ID_MAILBOX=$(tandem paths relay --root "$ID_ROOT") \
    || die "the installed tandem is behind these commands (no 'tandem paths'); re-run install.sh."
  if [[ "$ID_MAILBOX" == "$MAILBOX" ]]; then
    ID_MAILBOX=""
  fi
}

# find_reviewer: only the pane recorded in reviewer.json is closed.
find_reviewer() {
  REVIEWER_NAME=""
  rc=0
  _rrec=$(tandem reviewer show --relay "${ID_MAILBOX:-$MAILBOX}" 2>/dev/null) || rc=$?
  case $rc in
    0)  IFS=$'\t' read -r _ REVIEWER_NAME _ _ <<<"$_rrec" ;;
    28) ;;
    *)  info "WARNING the reviewer record in ${ID_MAILBOX:-$MAILBOX} cannot be read, so no reviewer pane is closed." ;;
  esac
  REVIEWER_PANE=""; REVIEWER_STATUS="unknown"
  if [[ -n "$REVIEWER_NAME" ]]; then
    rc=0
    _snap=$(tandem agent snapshot "$REVIEWER_NAME" 2>&1) || rc=$?
    case "$rc" in
      0) IFS=$'\t' read -r REVIEWER_PANE _ REVIEWER_STATUS <<<"$_snap" ;;
      10) ;;
      *)  die "could not find the reviewer '$REVIEWER_NAME': $(printf '%s' "$_snap" | cut -f2-)" ;;
    esac
    if [[ -n "$REVIEWER_PANE" && ( "$REVIEWER_PANE" == "$SELF_PANE" || "$REVIEWER_PANE" == "$BUILDER_PANE" ) ]]; then
      die "the reviewer '$REVIEWER_NAME' is this pane or the builder's pane, so nothing was closed."
    fi
  fi
}

# find_bridge: the relay bridge's pid from bridge.pid, when it is a number.
find_bridge() {
  BRIDGE_PID_FILE="$MAILBOX/bridge.pid"
  BRIDGE_PID=""
  if [[ -f "$BRIDGE_PID_FILE" ]]; then
    BRIDGE_PID=$(cat "$BRIDGE_PID_FILE" 2>/dev/null || true)
    [[ "$BRIDGE_PID" =~ ^[0-9]+$ ]] || BRIDGE_PID=""
  fi
}

# The OS reuses pids, so confirm the pid is still a tandem-bridge before
# signalling it.
bridge_is_ours() {
  local pid="$1" cmd
  [[ -n "$pid" ]] || return 1
  cmd=$(ps -p "$pid" -o command= 2>/dev/null || true)
  [[ "$cmd" == *tandem-bridge* ]]
}

# read_durable_id: the active durable run, if any. Its reports are not gated.
read_durable_id() {
  REPORTS_OK=()
  REPORTS_PENDING=()
  # A durable run is signed off on its own evidence from the state home, not on
  # handback reports. Reports beside it come from an earlier legacy build.
  DURABLE_ID=""
  if [[ -f "$MAILBOX/active-run.json" ]]; then
    DURABLE_ID=$(tandem state active-run --relay "$MAILBOX" 2>/dev/null) || DURABLE_ID=""
    [[ -n "$DURABLE_ID" ]] || DURABLE_ID="(unreadable marker)"
  fi
}

# report_pair: print what will be closed and which handbacks are signed off.
report_pair() {
  if [[ -n "$BUILDER_PANE" ]]; then
    info "agent    $BUILDER_NAME  pane $BUILDER_PANE  status: $BUILDER_STATUS"
  else
    info "agent    no agent named '$BUILDER_NAME', so there is nothing to close."
  fi
  if [[ -n "$REVIEWER_PANE" ]]; then
    info "agent    $REVIEWER_NAME  pane $REVIEWER_PANE  status: $REVIEWER_STATUS"
  elif [[ -n "$REVIEWER_NAME" ]]; then
    info "agent    no agent named '$REVIEWER_NAME', so there is no reviewer to close."
  fi
  if [[ -n "$BRIDGE_PID" ]] && kill -0 "$BRIDGE_PID" 2>/dev/null; then
    if bridge_is_ours "$BRIDGE_PID"; then
      info "bridge   pid $BRIDGE_PID"
    else
      info "bridge   pid $BRIDGE_PID in $BRIDGE_PID_FILE is not a tandem-bridge, so it was left running."
      BRIDGE_PID=""
    fi
  else
    info "bridge   not running"
    BRIDGE_PID=""
  fi
  if [[ -d "$MAILBOX" ]]; then
    # Globs, not `find | wc -l`: find fails on a missing reports/ under pipefail.
    shopt -s nullglob
    _q=( "$MAILBOX"/q/*.json )
    _r=( "$MAILBOX"/reports/*.md )
    shopt -u nullglob
    pending=${#_q[@]}
    reports=${#_r[@]}
    info "mailbox  $MAILBOX ($pending queued, $reports reports)"

    # Sign-off is per run and does not persist across a refusal, because the tree
    # may have changed since. ${arr+"${arr[@]}"} avoids bash 3.2's unbound-variable
    # error on an empty array under set -u.
    for _f in ${_r+"${_r[@]}"}; do
      _id=$(basename "$_f" .md)
      _ack="no"
      for _v in ${VERIFIED+"${VERIFIED[@]}"}; do
        [[ "$_v" == "$_id" ]] && { _ack="yes"; break; }
      done
      [[ -f "$MAILBOX/reports/$_id.verified" ]] && _ack="yes"
      if [[ -n "$DURABLE_ID" ]]; then
        info "handback $_id  left by an earlier legacy build; not part of durable run $DURABLE_ID, so not gated"
      elif [[ "$_ack" == yes ]]; then
        info "handback $_id  signed off"
        REPORTS_OK+=( "$_id" )
      else
        info "handback $_id  NOT signed off"
        REPORTS_PENDING+=( "$_id" )
      fi
    done
    # Do not delete unread reports without saying so.
    if (( ! KEEP_RELAY )) && (( reports > 0 )); then
      info "         removing it deletes those reports; --keep-relay keeps them."
    fi
  else
    info "mailbox  none at $MAILBOX"
  fi
}

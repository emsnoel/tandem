# shellcheck shell=bash
# bin/lib/pane-activity.sh: show in Herdr's sidebar what each pane works on.
#
# Sourced, never run. The text goes in the pane's `activity` token through
# `herdr pane report-metadata`, which leaves the pane's name alone. The sidebar
# shows it where config.toml names `$activity`. An empty text clears the token,
# so the pane shows only its plain label again. Every function returns 0, so a
# failed Herdr call never fails a delivery, a tick or a review.
#
# The caller sets MAILBOX to the relay. The last text written for each agent is
# kept in MAILBOX/activity/NAME, so a text that has not changed costs no Herdr
# call.

# ACTIVITY_MAX bounds the text, so it fits the sidebar at its widest (36
# columns) beside the state icon.
ACTIVITY_MAX=30

# plan_short FILE MAX: FILE's name for a label. It drops the folder, the
# leading date and the .md, turns each - into a space and keeps MAX characters.
# 2026-11-17-count-plan.md gives "count plan".
plan_short() {
  local s="${1##*/}"
  s="${s%.md}"
  [[ "$s" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}-(.+)$ ]] && s="${BASH_REMATCH[1]}"
  s="${s//-/ }"
  s="${s:0:$2}"
  s="${s%"${s##*[! ]}"}"
  printf '%s' "$s"
}

# with_plan PREFIX PLAN: PREFIX then PLAN's short name, cut to ACTIVITY_MAX.
with_plan() {
  printf '%s%s' "$1" "$(plan_short "$2" $(( ACTIVITY_MAX - ${#1} )))"
}

# run_activity STATE NUMBER COUNT PLAN: set ACT_BUILDER and ACT_PLANNER for a
# run in STATE on task NUMBER of COUNT. A run that is neither running nor
# paused has no work left, so both are empty.
run_activity() {
  ACT_BUILDER=""; ACT_PLANNER=""
  case "$1" in
    running) ACT_PLANNER="waiting for handback" ;;
    paused)  ACT_PLANNER="run paused" ;;
    *)       return 0 ;;
  esac
  if [[ -n "$2" && -n "$3" ]]; then
    ACT_BUILDER=$(with_plan "task $2/$3 · " "$4")
  else
    ACT_BUILDER=$(with_plan "" "$4")
  fi
}

# activity_set NAME TEXT [PANE]: show TEXT on agent NAME's pane, or clear the
# token when TEXT is empty. NAME must be a Herdr agent name, since it names a
# file and a verdict's sender comes from a queued file. The pane is looked up when PANE is not given. TEXT
# is recorded as written unless the Herdr socket was unreachable (exit 3), so
# the next call tries again. A gone pane or a refused call is recorded too:
# retrying it on every pass would only repeat the failure.
activity_set() {
  local name="$1" text="$2" pane="${3:-}" snap rc=0
  [[ "$name" =~ ^[A-Za-z0-9_-]+$ ]] || return 0
  if [[ -z "$pane" ]]; then
    snap=$(tandem agent snapshot "$name" 2>/dev/null) || rc=$?
    (( rc == 3 )) && return 0
    (( rc == 0 )) && IFS=$'\t' read -r pane _ <<<"$snap"
  fi
  if [[ -n "$pane" ]]; then
    if [[ -n "$text" ]]; then
      tandem pane report-metadata "$pane" --source tandem --token "activity=$text" >/dev/null 2>&1 || rc=$?
    else
      tandem pane report-metadata "$pane" --source tandem --clear-token activity >/dev/null 2>&1 || rc=$?
    fi
    (( rc == 3 )) && return 0
  fi
  { mkdir -p "$MAILBOX/activity" && printf '%s\n' "$text" > "$MAILBOX/activity/$name"; } 2>/dev/null || true
  return 0
}

# activity_sync NAME TEXT: activity_set, only when TEXT differs from the text
# last recorded for NAME. No record counts as empty.
activity_sync() {
  local last=""
  [[ "$1" =~ ^[A-Za-z0-9_-]+$ ]] || return 0
  last=$(cat "$MAILBOX/activity/$1" 2>/dev/null) || last=""
  [[ "$last" == "$2" ]] || activity_set "$1" "$2"
  return 0
}

# show_run_activity RUN: label the builder and the planner with RUN's task and
# plan, or clear them when RUN is empty. tandem-bridge calls it once a pass, so
# it reads the run's status and calls Herdr only when a text changes. The
# status fields are state, code, dispatch, reason, task, builder, reviewer,
# plan, collaboration, task number and task count. It needs FIELDS and
# read_field from tandem-bridge.
show_run_activity() {
  local state="" plan="" num="" count="" _
  if [[ -n "$1" ]] && tandem build status --root "$(dirname "$MAILBOX")" --fields >"$FIELDS" 2>/dev/null; then
    { read_field state; read_field _; read_field _; read_field _; read_field _; read_field _; read_field _
      read_field plan; read_field _; read_field num; read_field count; } <"$FIELDS"
  fi
  run_activity "$state" "$num" "$count" "$plan"
  activity_sync "$BUILDER_NAME" "$ACT_BUILDER"
  activity_sync "$PLANNER_NAME" "$ACT_PLANNER"
}

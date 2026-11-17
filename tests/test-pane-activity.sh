#!/usr/bin/env bash
# Each pane's `activity` token says what the pane works on (issue #58):
# bin/lib/pane-activity.sh on its own, then the bridge's hook through --once.
#
# Hermetic: tandem and herdr are stubs that log their argv. The bridge test
# passes relay reads to a real tandem built here.
set -uo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
LIB="$ROOT/bin/lib/pane-activity.sh"

PASS=0; FAIL=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }
# eq NAME GOT WANT
eq()   { if [ "$2" = "$3" ]; then ok "$1"; else nope "$1 (got '$2', want '$3')"; fi; }
# logged NAME LINE / unlogged NAME TEXT: whether CALLS has the line or the text.
logged()   { if grep -qxF -- "$2" "$CALLS"; then ok "$1"; else nope "$1 (no line: $2)"; sed 's/^/       | /' "$CALLS"; fi; }
unlogged() { if grep -qF -- "$2" "$CALLS"; then nope "$1 (found: $2)"; else ok "$1"; fi; }

TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
mkdir -p "$TMP/bin"
CALLS="$TMP/calls"; export CALLS
(cd "$ROOT" && go build -o "$TMP/tandem-real" ./cmd/tandem) || { echo "could not build tandem" >&2; exit 2; }
export TANDEM_REAL="$TMP/tandem-real"

# The tandem stub. STATUS is a file with the run's status fields, one per line.
# SNAP_RC and META_RC set the exit of `agent snapshot` and `pane
# report-metadata`. ACTIVE, when set, is the active run's id.
cat > "$TMP/bin/tandem" <<'STUB'
#!/usr/bin/env bash
case "$1 $2" in
  "relay read"|"paths names") exec "$TANDEM_REAL" "$@" ;;
  "state active-run") printf '%s\n' "${ACTIVE:-}"; exit 0 ;;
esac
printf '%s\n' "$*" >> "$CALLS"
case "$1 $2" in
  "agent snapshot")
    [ "${SNAP_RC:-0}" = 0 ] || exit "$SNAP_RC"
    printf 'w1:%s\t/repo\tidle\n' "$3" ;;
  "pane report-metadata") exit "${META_RC:-0}" ;;
  "build status")
    [ -f "${STATUS:-}" ] || exit 1
    tr '\n' '\0' < "$STATUS" ;;
  "build tick"|"build recover") printf '%s\0running\0\0\0' "$ACTIVE" ;;
  "agent status") printf 'idle\n' ;;
esac
exit 0
STUB
chmod +x "$TMP/bin/tandem"
printf '#!/bin/sh\nexit 0\n' > "$TMP/bin/herdr"; chmod +x "$TMP/bin/herdr"
export PATH="$TMP/bin:$PATH"

# status STATE NUMBER COUNT PLAN: write the run's status fields.
STATUS="$TMP/status"; export STATUS
status() { printf '%s\n' "$1" "" "" "" task-x builder reviewer "$4" "" "$2" "$3" > "$STATUS"; }

echo "--- plan names ---"
# shellcheck source=../bin/lib/pane-activity.sh
. "$LIB"
eq "the date and .md go, and - turns into a space" "$(plan_short docs/plans/2026-11-17-count-plan.md 30)" "count plan"
eq "a name with no date keeps its words" "$(plan_short /x/my-plan.md 30)" "my plan"
eq "a long name is cut, with no space left at the end" "$(plan_short 2026-01-02-alpha-beta-gamma.md 11)" "alpha beta"

echo "--- texts for a run ---"
run_activity running 2 3 docs/2026-11-17-count-plan.md
eq "a running builder names its task and plan" "$ACT_BUILDER" "task 2/3 · count plan"
eq "the planner waits for the handback" "$ACT_PLANNER" "waiting for handback"
run_activity paused 2 3 docs/2026-11-17-count-plan.md
eq "a paused run keeps the builder's task" "$ACT_BUILDER" "task 2/3 · count plan"
eq "and tells the planner it is paused" "$ACT_PLANNER" "run paused"
run_activity finished 3 3 docs/2026-11-17-count-plan.md
eq "a finished run clears the builder" "$ACT_BUILDER" ""
eq "and the planner" "$ACT_PLANNER" ""
run_activity running 12 14 2026-11-17-a-very-long-plan-name-that-goes-on-and-on.md
[ "$(printf '%s' "$ACT_BUILDER" | LC_ALL=en_US.UTF-8 wc -m | tr -d ' ')" -le 30 ] \
  && ok "the text fits in 30 characters ($ACT_BUILDER)" || nope "the text is too long: $ACT_BUILDER"

echo "--- the bridge's pass writes only what changed ---"
MAILBOX="$TMP/relay"; mkdir -p "$MAILBOX"
FIELDS="$TMP/fields"
read_field() { IFS= read -r -d '' "$1" || true; }
BUILDER_NAME=builder; PLANNER_NAME=planner
status running 2 3 docs/2026-11-17-count-plan.md
: > "$CALLS"; show_run_activity r1
logged "the builder's pane gets its task" "pane report-metadata w1:builder --source tandem --token activity=task 2/3 · count plan"
logged "the planner's pane says it waits" "pane report-metadata w1:planner --source tandem --token activity=waiting for handback"
: > "$CALLS"; show_run_activity r1
unlogged "an unchanged run calls no Herdr command" "report-metadata"
unlogged "and looks up no pane" "agent snapshot"
status running 3 3 docs/2026-11-17-count-plan.md
: > "$CALLS"; show_run_activity r1
logged "the next task updates the builder" "pane report-metadata w1:builder --source tandem --token activity=task 3/3 · count plan"
unlogged "and leaves the planner alone" "w1:planner"
: > "$CALLS"; show_run_activity ""
logged "with no run the builder goes back to plain" "pane report-metadata w1:builder --source tandem --clear-token activity"
logged "and so does the planner" "pane report-metadata w1:planner --source tandem --clear-token activity"
: > "$CALLS"; show_run_activity ""
unlogged "a second pass with no run calls nothing" "report-metadata"

echo "--- failures never fail the caller ---"
rm -rf "$MAILBOX/activity"
( set -e; META_RC=1 activity_set builder "task 1/2 · x"; echo survived ) | grep -q survived \
  && ok "a refused metadata call returns 0 under set -e" || nope "a refused metadata call failed its caller"
( set -e; SNAP_RC=10 activity_set builder ""; echo survived ) | grep -q survived \
  && ok "a gone pane returns 0 under set -e" || nope "a gone pane failed its caller"
rm -rf "$MAILBOX/activity"
SNAP_RC=3 activity_sync builder "task 1/2 · x"
[ ! -e "$MAILBOX/activity/builder" ] && ok "an unreachable socket records nothing, so the next pass tries again" \
  || nope "an unreachable socket was recorded as written"
META_RC=3 activity_sync builder "task 1/2 · x"
[ ! -e "$MAILBOX/activity/builder" ] && ok "so does a metadata call that cannot reach the socket" \
  || nope "a metadata call that could not reach the socket was recorded"
: > "$CALLS"; META_RC=1 activity_sync builder "task 1/2 · x"; activity_sync builder "task 1/2 · x"
[ "$(grep -c report-metadata "$CALLS")" = 1 ] && ok "a refused call is not retried on every pass" \
  || nope "a refused call was retried: $(cat "$CALLS")"

: > "$CALLS"; activity_sync "../escape" "x"
[ ! -e "$TMP/escape" ] && [ ! -s "$CALLS" ] && ok "a name that is not an agent name is ignored" \
  || nope "a name with a path in it was used"

echo "--- the bridge ---"
# A live run labels both panes; a verdict clears the reviewer that sent it.
BRIDGE="$ROOT/bin/tandem-bridge"
MB="$TMP/repo/.tandem-relay"; mkdir -p "$MB/q" "$MB/durable/run1"
printf '{}\n' > "$MB/active-run.json"
status running 1 2 docs/2026-11-17-count-plan.md
bridge() { : > "$CALLS"; OUT=$(HERDR_ENV=1 "$BRIDGE" --mailbox "$MB" --once 2>&1); RC=$?; }
ACTIVE=run1 bridge
[ "$RC" = 0 ] && ok "the bridge exits 0" || nope "the bridge exits 0 (rc $RC: $OUT)"
logged "the bridge labels the builder" "pane report-metadata w1:builder --source tandem --token activity=task 1/2 · count plan"
logged "and the planner" "pane report-metadata w1:planner --source tandem --token activity=waiting for handback"
ACTIVE=run1 bridge
unlogged "the next pass of the same run calls no Herdr command" "report-metadata"
rm -f "$MB/active-run.json"
mkdir -p "$MB/activity"; printf 'reviewing count plan\n' > "$MB/activity/reviewer"
jq -n '{id:"v1", kind:"verdict", from:"reviewer", question:"verdict", message:"returned: clean"}' > "$MB/q/v1.json"
bridge
logged "a run that ended clears the builder" "pane report-metadata w1:builder --source tandem --clear-token activity"
logged "a delivered verdict clears the reviewer" "pane report-metadata w1:reviewer --source tandem --clear-token activity"

printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

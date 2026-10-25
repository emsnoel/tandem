#!/usr/bin/env bash
# tandem-handoff on a durable run.
#
# The run delivers its own prompts, so handoff only starts or continues it and
# ticks once. It must not send a prompt, reset the builder or write a legacy
# build record, and a legacy start must not get flags it does not know.
#
# Herdr and `tandem build` are stubbed and the start answer is scripted.
set -uo pipefail
. "$(cd "$(dirname "$0")" && pwd)/lib/records.sh"
ROOT_DIR=$(cd "$(dirname "$0")/.." && pwd)
HANDOFF="$ROOT_DIR/bin/tandem-handoff"

PASS=0; FAIL=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }
has()   { case "$2" in *"$3"*) ok "$1" ;; *) nope "$1 (missing: $3)" ;; esac; }

command -v jq >/dev/null || { echo "need jq" >&2; exit 2; }
TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
REPO="$TMP/repo"; RELAY="$REPO/.tandem-relay"
mkdir -p "$TMP/bin" "$REPO/docs/plans" "$RELAY"
git -C "$REPO" init -q
printf '.tandem-relay/\n' > "$REPO/.gitignore"
PLAN="$REPO/docs/plans/p.md"
printf '# Plan\n\n**Goal:** g.\n\n### Task 1: One\n\nDo one.\n' > "$PLAN"
write_builder "$RELAY"
# A run needs a reviewer pane: the builder never reviews.
write_reviewer "$RELAY"

ln -s "$(command -v jq)" "$TMP/bin/jq"
printf '#!/usr/bin/env bash\nexit 0\n' > "$TMP/bin/herdr"
printf '#!/usr/bin/env bash\nexit 0\n' > "$TMP/bin/tandem-review"
cat > "$TMP/bin/tandem" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$CALLS"
case "$1" in
  paths) printf '%s\n' "$RELAY"; exit 0 ;;
  # The review gate refuses a pair with no reviewer before anything else.
  gate) [ -f "$RELAY/reviewer.json" ] && exit 0; printf 'no_reviewer\n'; exit 24 ;;
  build)
    case "$2" in
      start) printf '%s\n' "$START_OUT"; [ -n "${START_ERR:-}" ] && printf '%s\n' "$START_ERR" >&2; exit "${START_RC:-0}" ;;
      tick)  printf '{"run_id":"r"}\n'; exit "${TICK_RC:-0}" ;;
      # What the run reports after its tick; by default the first task was sent.
      # The wait exits 23 at its timeout with the last status.
      wait)
        sent='{"state":"running","tasks":[{"accepted":false,"attempts":[{"current":true,"dispatch":"sent"}]}]}'
        out="${STATUS_OUT:-$sent}"
        [ -n "${WAIT_ERR:-}" ] && { printf '%s\n' "$WAIT_ERR" >&2; exit 1; }
        printf '%s\n' "$out"
        printf '%s' "$out" | jq -e '.state == "running" and ([.tasks[]? | .attempts[]? | select(.current) | .dispatch] | index("sent") | not)' >/dev/null && exit 23
        exit 0 ;;
      status)
        sent='{"state":"running","tasks":[{"accepted":false,"attempts":[{"current":true,"dispatch":"sent"}]}]}'
        printf '%s\n' "${STATUS_OUT:-$sent}"; exit 0 ;;
    esac
    exit 0 ;;
esac
case "$1 $2" in
  "agent snapshot") printf '%%1\t%s\tidle\n' "$REPO"; exit 0 ;;
  "agent status")   printf 'idle\n'; exit 0 ;;
  "agent session")  printf 's1\n'; exit 0 ;;
  "agent reset")    printf 'reset\n'; exit 0 ;;
esac
exit 0
STUB
cat > "$TMP/bin/tandem-project" <<'STUB'
#!/usr/bin/env bash
jq -n --arg root "$REPO" --argjson ctx "${PROJ_CTX:-0}" --arg eng "${PROJ_ENGINE:-}" '{root:$root, plans_dir:"docs/plans", check_command:"./check.sh",
  conventions_file:"AGENTS.md", prompt_extra:"", task_input_budget:3000000, context_limit:$ctx, build_engine:$eng, source:"stub"}'
STUB
chmod +x "$TMP/bin/"*

CALLS="$TMP/calls"
LEGACY='{"task":1,"total":1,"title":"One","brief":"/b/1.md","ledger":"","resumed":false}'
DURABLE='{"engine":"durable","run_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}'
run() { # run <env assignments...> -- <handoff args...>
  local envs=()
  while [[ "$1" != -- ]]; do envs+=("$1"); shift; done; shift
  : > "$CALLS"
  OUT=$(env PATH="$TMP/bin:$PATH" HERDR_ENV=1 REPO="$REPO" RELAY="$RELAY" CALLS="$CALLS" "${envs[@]}" "$HANDOFF" "$PLAN" "$@" 2>&1); RC=$?
}

printf '\n== a durable start ==\n'
run TANDEM_BUILD_ENGINE=durable START_OUT="$DURABLE" --
[ "$RC" -eq 0 ] && ok "a durable start exits 0 (rc $RC)" || nope "durable start rc $RC: $OUT"
grep -q '^build start .*--builder-kind codex' "$CALLS" && ok "start is told the builder kind" || nope "no --builder-kind ($(cat "$CALLS"))"
grep -q "^build tick --root $REPO" "$CALLS" && ok "the run gets its first tick" || nope "no tick ($(cat "$CALLS"))"
grep -qE '^agent (prompt|reset)|^build sent' "$CALLS" && nope "handoff sent, reset or recorded something itself ($(cat "$CALLS"))" || ok "handoff sends no prompt, resets nothing and writes no legacy record"
has "it names the run" "$OUT" "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

printf '\n== handoff waits for the first task to land ==\n'
status() { # status STATE PAUSE_CODE DISPATCH [REASON]
  jq -cn --arg s "$1" --arg c "$2" --arg d "$3" --arg r "${4:-}" \
    '{state:$s, pause_code:$c, pause_reason:$r, tasks:[{accepted:false, attempts:[{current:true, dispatch:$d}]}]}'
}
run TANDEM_BUILD_ENGINE=durable START_OUT="$DURABLE" STATUS_OUT="$(status running "" sent)" --
[ "$RC" -eq 0 ] && has "a task that was sent: the run started" "$OUT" "started for builder" || nope "sent: rc $RC: $OUT"
grep -q "^build wait --root $REPO --until sent" "$CALLS" && ok "it waits on the run for the task to land" || nope "no wait ($(cat "$CALLS"))"

REASON="builder's turn ended as soon as it started. Its pane shows:
There's an issue with the selected model (sonnet-5.5)."
run TANDEM_BUILD_ENGINE=durable START_OUT="$DURABLE" STATUS_OUT="$(status paused dispatch_failed failed "$REASON")" --
[ "$RC" -eq 8 ] && ok "a task that could not be delivered exits 8" || nope "dispatch_failed: rc $RC: $OUT"
has "it shows why" "$OUT" "There's an issue with the selected model (sonnet-5.5)."
has "and the way on" "$OUT" "tandem build stop --root $REPO"
case "$OUT" in *"started for builder"*) nope "it still says the run started" ;; *) ok "and does not say the run started" ;; esac

run TANDEM_BUILD_ENGINE=durable START_OUT="$DURABLE" STATUS_OUT="$(status paused plan_changed pending)" --
[ "$RC" -eq 23 ] && has "a run that paused for another reason needs a person" "$OUT" "needs a person" || nope "other pause: rc $RC: $OUT"

run TANDEM_BUILD_ENGINE=durable START_OUT="$DURABLE" STATUS_OUT="$(status running "" pending)" TANDEM_DISPATCH_WAIT_S=2 --
[ "$RC" -eq 0 ] && has "a task still pending when the wait ends is not claimed" "$OUT" "not confirmed" || nope "pending: rc $RC: $OUT"
# One tick starts the run and the bridge drives it. More ticks here would hold
# the run's lease and starve the bridge.
[ "$(grep -c '^build tick' "$CALLS")" -eq 1 ] && ok "it ticks once and only polls while it waits" || nope "it ticked $(grep -c '^build tick' "$CALLS") times"
[ "$(grep -c '^build wait' "$CALLS")" -eq 1 ] && ok "and waits once, in the binary, not in a script loop" || nope "it waited $(grep -c '^build wait' "$CALLS") times ($(cat "$CALLS"))"

run TANDEM_BUILD_ENGINE=durable START_OUT="$DURABLE" STATUS_OUT="$(status stopping "" pending)" --
[ "$RC" -eq 0 ] && has "a run that is stopping returns at once" "$OUT" "is stopping." || nope "stopping: rc $RC: $OUT"
grep -q -- '--until-not running' "$CALLS" && ok "the wait is told to end on any state but running" || nope "no --until-not running ($(cat "$CALLS"))"
run TANDEM_BUILD_ENGINE=durable START_OUT="$DURABLE" STATUS_OUT="" WAIT_ERR="tandem build: no durable run in $RELAY" --
[ "$RC" -eq 0 ] && has "a wait that fails says it could not read the run" "$OUT" "could not read durable run aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa: tandem build: no durable run" || nope "wait error: rc $RC: $OUT"
case "$OUT" in *"not confirmed as sent"*) nope "a failed wait claimed a timeout" ;; *) ok "and does not claim the task timed out" ;; esac

printf '\n== a failing start or tick ==\n'
run TANDEM_BUILD_ENGINE=durable START_OUT="" START_RC=1 --
[ "$RC" -ne 0 ] && ok "a start that fails fails the handoff" || nope "start failure ignored"
grep -q '^build tick' "$CALLS" && nope "a run that did not start was ticked" || ok "and nothing is ticked"
run TANDEM_BUILD_ENGINE=durable START_OUT="$DURABLE" TICK_RC=3 --
[ "$RC" -eq 7 ] && ok "a first tick that fails exits 7: the run exists but nothing was delivered" || nope "tick failure rc $RC: $OUT"
has "and says the bridge or recover continues it" "$OUT" "tandem build recover"

run TANDEM_BUILD_ENGINE=durable START_OUT="" START_RC=1 START_ERR="tandem build: an unfinished legacy build (stopped) in $RELAY/build owns this relay" --
has "a refused start shows tandem build's own reason" "$OUT" "an unfinished legacy build (stopped)"
has "and points at it, not at the install first" "$OUT" "the reason is above"

printf '\n== build_engine in .tandem.json ==\n'
run PROJ_ENGINE=durable START_OUT="$DURABLE" --
grep -q '^build start .*--builder-kind codex' "$CALLS" && ok "a repo that says durable starts durable with no variable set" || nope "build_engine ignored ($(cat "$CALLS"))"
run PROJ_ENGINE=durable TANDEM_BUILD_ENGINE=legacy START_OUT="$LEGACY" -- --dry-run
grep '^build start' "$CALLS" | grep -q -- '--builder-kind' && nope "the variable did not override the file ($(cat "$CALLS"))" || ok "TANDEM_BUILD_ENGINE=legacy overrides the file"
run START_OUT="$LEGACY" -- --dry-run
grep '^build start' "$CALLS" | grep -q -- '--builder-kind' && nope "no setting still started durable" || ok "with neither, the default stays legacy"

printf '\n== a durable dry run ==\n'
run TANDEM_BUILD_ENGINE=durable START_OUT='{"dry_run":true,"engine":"durable"}' -- --dry-run
[ "$RC" -eq 0 ] && ok "a durable dry run exits 0 (rc $RC)" || nope "durable dry run rc $RC: $OUT"
has "and says a durable run would start" "$OUT" "[dry-run] a durable run would start"
case "$OUT" in *install.sh*) nope "a durable dry run blamed the install: $OUT" ;; *) ok "and does not blame the install" ;; esac
grep -q '^build tick' "$CALLS" && nope "a dry run ticked the run" || ok "and ticks nothing"
run START_OUT='{"dry_run":true,"engine":"durable","run_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}' -- --continue --dry-run
[ "$RC" -eq 0 ] && has "a dry continue says the run would continue" "$OUT" "would continue" || nope "dry continue rc $RC: $OUT"
grep -q '^build tick' "$CALLS" && nope "a dry continue ticked the run" || ok "and ticks nothing"

printf '\n== the context limit ==\n'
run TANDEM_BUILD_ENGINE=durable START_OUT="$DURABLE" -- --context-limit 20000
[ "$RC" -eq 0 ] && grep -q '^build start .*--context-limit 20000' "$CALLS" && ok "--context-limit reaches a durable start" || nope "flag: rc $RC $OUT ($(cat "$CALLS"))"
run TANDEM_BUILD_ENGINE=durable START_OUT="$DURABLE" PROJ_CTX=15000 --
grep -q '^build start .*--context-limit 15000' "$CALLS" && ok "context_limit in .tandem.json reaches it too" || nope "project key ($(cat "$CALLS"))"
run TANDEM_BUILD_ENGINE=durable START_OUT="$DURABLE" PROJ_CTX=15000 -- --context-limit 30000
grep -q '^build start .*--context-limit 30000' "$CALLS" && ! grep -q -- '--context-limit 15000' "$CALLS" && ok "the flag wins over the project setting" || nope "precedence ($(cat "$CALLS"))"
run TANDEM_BUILD_ENGINE=durable START_OUT="$DURABLE" --
grep '^build start' "$CALLS" | grep -q -- '--context-limit' && nope "an unset limit was passed ($(cat "$CALLS"))" || ok "with neither, start keeps its own default"
run START_OUT="$LEGACY" PROJ_CTX=15000 -- --context-limit 20000 --dry-run
grep '^build start' "$CALLS" | grep -q -- '--context-limit' && nope "a legacy start was handed --context-limit" || ok "a legacy start is not handed --context-limit"
run TANDEM_BUILD_ENGINE=durable START_OUT="$DURABLE" -- --context-limit lots
[ "$RC" -ne 0 ] && has "a limit that is not a number is refused" "$OUT" "--context-limit" || nope "bad limit rc $RC"
grep -q '^build start' "$CALLS" && nope "and start was still called" || ok "before anything starts"
run TANDEM_BUILD_ENGINE=durable START_OUT="" START_RC=23 START_ERR="tandem build: mandatory context too large: 1 of 2 tasks do not fit" --
[ "$RC" -eq 23 ] && has "a start refused for size says no run was started" "$OUT" "no durable run was started" || nope "size refusal rc $RC: $OUT"
case "$OUT" in *"was created but is paused"*) nope "it claims a run was created: $OUT" ;; *) ok "and does not claim a run exists" ;; esac
has "and names both ways to raise the limit" "$OUT" "context_limit in .tandem.json"

printf '\n== continuing ==\n'
run START_OUT='{"run_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","state":"running"}' -- --continue
[ "$RC" -eq 0 ] && ok "--continue on a durable run exits 0" || nope "continue rc $RC: $OUT"
grep -q '^build start .*--resume-only' "$CALLS" && ok "continue asks start to resume only" || nope "no --resume-only"
grep -q '^build tick' "$CALLS" && ok "and ticks" || nope "no tick on continue"

run START_OUT='{"run_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","state":"paused","pause_code":"plan_changed"}' START_RC=23 -- --continue
[ "$RC" -eq 23 ] && has "a paused run is reported as needing a person, not as nothing to continue" "$OUT" "needs a person" || nope "paused continue: rc $RC: $OUT"
grep -q '^build tick' "$CALLS" && nope "a paused run was ticked by handoff" || ok "and handoff does not tick it"

run TANDEM_BUILD_ENGINE=durable START_OUT='{"run_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","state":"paused","pause_code":"context_too_large"}' START_RC=23 --
[ "$RC" -eq 23 ] && has "a start that paused at a gate says so, not 're-run install.sh'" "$OUT" "needs a person" || nope "paused start: rc $RC: $OUT"
case "$OUT" in *install.sh*) nope "a paused start blamed the install" ;; *) ok "and does not blame the install" ;; esac

printf '\n== a reviewer pane ==\n'
rm -f "$RELAY/reviewer.json"
run TANDEM_BUILD_ENGINE=durable START_OUT="$DURABLE" --
[ "$RC" -eq 24 ] && ok "a durable handoff with no reviewer is refused (rc $RC)" || nope "no reviewer: rc $RC: $OUT"
has "and the refusal says the builder never reviews" "$OUT" "the builder never reviews"
grep -q '^build start' "$CALLS" && nope "a run was started with no reviewer ($(cat "$CALLS"))" || ok "and no run is created"
[ -e "$RELAY/active-run.json" ] && nope "a refused handoff left a run marker in $RELAY" || ok "and no marker is written"
# The start refuses a half-written reviewer.json with 23 and the remedy, and
# handoff ticks nothing.
printf '{"kind":"claude"}\n' > "$RELAY/reviewer.json"
run TANDEM_BUILD_ENGINE=durable START_OUT="" START_RC=23 \
  START_ERR="tandem build start: the reviewer record cannot be read or names no reviewer kind and name: $RELAY/reviewer.json. Re-pair with a reviewer: declare a \"reviewer\" block in .tandem.json and re-run tandem-pair, or run tandem-pair --reviewer KIND" --
[ "$RC" -eq 23 ] && has "a reviewer.json with no name is refused with the remedy" "$OUT" "tandem-pair --reviewer KIND" || nope "half-written reviewer.json: rc $RC: $OUT"
has "and the refusal says nothing was sent" "$OUT" "Nothing was sent"
grep -q '^build tick' "$CALLS" && nope "a run was ticked from a half-written reviewer.json ($(cat "$CALLS"))" || ok "and no run is ticked"
case "$OUT" in *install.sh*) nope "a reviewer refusal blamed the install: $OUT" ;; *) ok "and does not blame the install" ;; esac
write_reviewer "$RELAY"
run TANDEM_BUILD_ENGINE=durable START_OUT="$DURABLE" --
grep '^build start' "$CALLS" | grep -q -- '--reviewer' && nope "handoff read reviewer.json itself and passed --reviewer ($(cat "$CALLS"))" \
  || ok "a durable start reads the recorded reviewer pane itself"
run START_OUT="$LEGACY" -- --dry-run
grep '^build start' "$CALLS" | grep -q -- '--reviewer' && nope "a legacy start was handed --reviewer" || ok "a legacy start is not handed --reviewer"

printf '\n== a legacy start ==\n'
run START_OUT="$LEGACY" -- --dry-run
grep -q '^build start' "$CALLS" && ! grep '^build start' "$CALLS" | grep -q -- '--builder-kind' \
  && ok "a legacy start is not handed --builder-kind" || nope "legacy start args ($(cat "$CALLS"))"
grep -q '^build tick' "$CALLS" && nope "a legacy run was ticked" || ok "a legacy run is never ticked"

printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

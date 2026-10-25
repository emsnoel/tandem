#!/usr/bin/env bash
# The durable engine end to end through the shipped binary and a fake Herdr.
#
# No model is called. The checks are what tandem types, to whom, and how often:
#   - the first tick types the task prompt once, with the run's nonce;
#   - a second tick types nothing more;
#   - a blocked agent gets nothing typed and the run says so;
#   - a stop reaches the working builder as one more message;
#   - a builder kind with no adapter is refused before any state exists.
set -uo pipefail
. "$(cd "$(dirname "$0")" && pwd)/lib/records.sh"
ROOT_DIR=$(cd "$(dirname "$0")/.." && pwd)

PASS=0; FAIL=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }

command -v jq >/dev/null || { echo "need jq" >&2; exit 2; }
TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
(cd "$ROOT_DIR" && go build -o "$TMP/bin/tandem" ./cmd/tandem) || { echo "could not build tandem"; exit 1; }
ln -s "$(command -v jq)" "$TMP/bin/jq"

. "$ROOT_DIR/tests/lib/fake-herdr.sh"
. "$ROOT_DIR/tests/lib/fake-socket.sh"
FAKE="$TMP/fake"
fake_herdr_install "$TMP/bin" "$FAKE"
FAKE="$FAKE" PATH="$TMP/bin:$PATH" fake_socket_start "$TMP" || exit 1

REPO="$TMP/repo"; mkdir -p "$REPO"; git -C "$REPO" init -q
git -C "$REPO" config user.email t@t; git -C "$REPO" config user.name t
printf '.tandem-relay/\n' > "$REPO/.gitignore"
mkdir -p "$REPO/.tandem-relay"; write_builder "$REPO/.tandem-relay"
printf '# Plan\n\n**Goal:** g.\n\n### Task 1: First\n\nfirst thing\n\n### Task 2: Second\n\nsecond thing\n' > "$REPO/plan.md"
git -C "$REPO" add . && git -C "$REPO" commit -qm plan
HOME_STATE="$TMP/state"
td() { env PATH="$TMP/bin:/usr/bin:/bin" FAKE="$FAKE" REPO="$REPO" TANDEM_BUILD_ENGINE=durable TANDEM_STATE_HOME="$HOME_STATE" tandem "$@"; }
prompts() { [ -f "$FAKE/prompts" ] && wc -l < "$FAKE/prompts" | tr -d ' ' || echo 0; }

printf '\n== a kind with no adapter is refused before anything exists ==\n'
td build start --plan "$REPO/plan.md" --root "$REPO" --builder-kind nonesuch --reviewer reviewer --reviewer-kind claude >/dev/null 2>"$TMP/err"; rc=$?
[ "$rc" -ne 0 ] && grep -q nonesuch "$TMP/err" && ok "start refuses an unknown kind" || nope "unknown kind rc $rc: $(cat "$TMP/err")"
[ ! -e "$HOME_STATE" ] && [ ! -e "$REPO/.tandem-relay/active-run.json" ] && ok "and creates no state and no marker" || nope "state or marker created"

printf '\n== start, then one tick types the task once ==\n'
OUT=$(td build start --plan "$REPO/plan.md" --root "$REPO" --builder builder --planner planner --builder-kind codex --reviewer reviewer --reviewer-kind claude --check true 2>"$TMP/err"); rc=$?
RUN=$(printf '%s' "$OUT" | jq -r .run_id 2>/dev/null)
[ "$rc" -eq 0 ] && [[ "$RUN" =~ ^[0-9a-f]{32}$ ]] && ok "a durable run starts with the shipped transport (run $RUN)" || nope "start rc $rc: $OUT $(cat "$TMP/err")"
[ "$(prompts)" -eq 0 ] && ok "start itself types nothing" || nope "start typed a prompt"
td build tick --root "$REPO" >/dev/null 2>"$TMP/err"
[ "$(prompts)" -eq 1 ] && ok "the first tick types exactly one prompt" || nope "prompts after tick: $(prompts) $(cat "$TMP/err")"
P=$(cut -f2- "$FAKE/prompts" | head -1); WHO=$(cut -f1 "$FAKE/prompts" | head -1)
[ "$WHO" = builder ] && ok "it goes to the builder pane" || nope "sent to $WHO"
case "$P" in *"first thing"*) ok "it carries the first task's brief" ;; *) nope "no brief in the prompt" ;; esac
case "$P" in *"$RUN"*) ok "it names the run, so the builder can hand back with the right identity" ;; *) nope "no run id in the prompt" ;; esac
case "$P" in *"second thing"*) nope "task 2 leaked into task 1's prompt" ;; *) ok "and holds only its own task" ;; esac
td build tick --root "$REPO" >/dev/null 2>&1; td build tick --root "$REPO" >/dev/null 2>&1
[ "$(prompts)" -eq 1 ] && ok "further ticks type nothing more while the builder works" || nope "prompts after more ticks: $(prompts)"
ST=$(td build status --root "$REPO" --json | jq -r '.state')
[ "$ST" = running ] && ok "status reads the run back as running" || nope "state $ST"

printf '\n== a stop reaches the working builder ==\n'
td build stop --root "$REPO" >/dev/null 2>&1; rc=$?
td build tick --root "$REPO" >/dev/null 2>&1
[ "$rc" -eq 0 ] && [ "$(prompts)" -eq 2 ] && tail -1 "$FAKE/prompts" | grep -q 'stopped' && ok "the stop notice is typed once into the working builder" || nope "stop: rc $rc prompts $(prompts)"
td build tick --root "$REPO" >/dev/null 2>&1
[ "$(prompts)" -eq 2 ] && ok "and not again" || nope "stop notice repeated"
ST=$(td build status --root "$REPO" --json | jq -r '.state')
[ "$ST" = stopping ] && ok "the run stays stopping while the builder is still working" || nope "state $ST"
fake_set_state idle
td build tick --root "$REPO" >/dev/null 2>&1
ST=$(td build status --root "$REPO" --json | jq -r '.state')
[ "$ST" = paused ] && ok "and pauses once the builder is seen idle" || nope "state after idle: $ST"

printf '\n== a blocked agent is never typed into ==\n'
REPO2="$TMP/repo2"; cp -R "$REPO" "$REPO2"; find "$REPO2/.tandem-relay" -mindepth 1 ! -name builder.json -delete 2>/dev/null
fake_set_state blocked; : > "$FAKE/prompts"
td2() { env PATH="$TMP/bin:/usr/bin:/bin" FAKE="$FAKE" REPO="$REPO2" TANDEM_BUILD_ENGINE=durable TANDEM_STATE_HOME="$TMP/state2" tandem "$@"; }
td2 build start --plan "$REPO2/plan.md" --root "$REPO2" --builder builder --builder-kind codex --reviewer reviewer --reviewer-kind claude --check true >/dev/null 2>&1
td2 build tick --root "$REPO2" >/dev/null 2>&1
[ "$(prompts)" -eq 0 ] && ok "nothing is typed into a dialog" || nope "typed into a blocked agent"
ST=$(td2 build status --root "$REPO2" --json | jq -r '.state')
[ "$ST" = running ] && ok "the run waits and retries instead of failing at once" || nope "state $ST"

printf '\n== two tasks to the end: ack, submit, check, review record, accept, final notice ==\n'
R3="$TMP/repo3"; cp -R "$REPO" "$R3"; find "$R3/.tandem-relay" -mindepth 1 ! -name builder.json -delete 2>/dev/null; rm -f "$FAKE/resets"; : > "$FAKE/prompts"; fake_set_state idle
td3() { env PATH="$TMP/bin:/usr/bin:/bin" FAKE="$FAKE" REPO="$R3" TANDEM_BUILD_ENGINE=durable TANDEM_STATE_HOME="$TMP/state3" tandem "$@"; }
tickn() { for _ in 1 2 3 4; do td3 build tick --root "$R3" >/dev/null 2>"$TMP/err"; done; }
field() { printf '%s' "$1" | grep -o -- "--$2 [0-9a-f]*" | head -1 | cut -d' ' -f2; }
jfield() { printf '%s' "$1" | grep -o "\"$2\":\"[0-9a-f]*\"" | head -1 | cut -d'"' -f4; }
td3 build start --plan "$R3/plan.md" --root "$R3" --builder builder --planner planner --builder-kind codex --reviewer reviewer --reviewer-kind claude --check true >/dev/null 2>&1
RUN3=$(td3 build status --root "$R3" --json | jq -r .run_id)

one_task() { # one_task N: the builder and the reviewer take their turns
  local n="$1" pl att job eff nonce rv rj ra rp rt
  tickn
  pl=$(sed -n "$(( (n-1)*2 + 1 ))p" "$FAKE/prompts" | cut -f2-)
  att=$(field "$pl" attempt); job=$(field "$pl" job); eff=$(field "$pl" effect); nonce=$(field "$pl" nonce)
  [ -n "$att" ] && [ -n "$nonce" ] || { nope "task $n: no identity in the prompt"; return 1; }
  td3 build event new --root "$R3" --kind builder_started --task "$n" --attempt "$att" --job "$job" --effect "$eff" --nonce "$nonce" >/dev/null 2>&1
  printf 'work for task %s\n' "$n" > "$R3/file$n.txt"
  printf 'done: task %s\n' "$n" > "$TMP/report$n.md"
  td3 build event new --root "$R3" --kind builder_submitted --task "$n" --attempt "$att" --job "$job" --effect "$eff" --nonce "$nonce" --payload "$TMP/report$n.md" >/dev/null 2>&1
  fake_set_state idle
  tickn
  rv=$(sed -n "$(( (n-1)*2 + 2 ))p" "$FAKE/prompts" | cut -f2-)
  rj=$(jfield "$rv" job_id); ra=$(jfield "$rv" attempt_id); rp=$(jfield "$rv" patch_hash); rt=$(jfield "$rv" tree_hash)
  [ -n "$rj" ] && [ "$ra" = "$att" ] || { nope "task $n: no review prompt bound to the attempt ($(td3 build status --root "$R3" | tr '\n' ' '))"; return 1; }
  printf '{"schema_version":1,"job_id":"%s","attempt_id":"%s","patch_hash":"%s","tree_hash":"%s","status":"passed","findings":[]}\n' "$rj" "$ra" "$rp" "$rt" > "$TMP/review$n.json"
  if [ "$n" = 1 ]; then
    fake_set_state working
    td3 build review-record --root "$R3" --result "$TMP/review$n.json" >/dev/null 2>"$TMP/err"; rc=$?
    [ "$rc" -ne 0 ] && grep -q 'not finished' "$TMP/err" && ok "a review is refused while the reviewer's pane is still working" || nope "review-record while working: rc $rc $(cat "$TMP/err")"
  fi
  fake_set_state idle
  td3 build review-record --root "$R3" --result "$TMP/review$n.json" >/dev/null 2>"$TMP/err" \
    && ok "task $n: the pane review is recorded against its job, patch and tree" || nope "task $n: review-record failed: $(cat "$TMP/err")"
  fake_set_state idle
  tickn
}
one_task 1
[ "$(prompts)" -eq 3 ] && sed -n 3p "$FAKE/prompts" | grep -q 'second thing' && ok "acceptance of task 1 dispatches task 2 once" || { nope "task 2 not dispatched (prompts $(prompts): $(cut -c1-80 "$FAKE/prompts" | tr '\n' '|'))"; }
ACC=$(td3 build status --root "$R3" --json | jq '[.tasks[] | select(.accepted)] | length')
[ "$ACC" -eq 1 ] && ok "status shows one accepted task" || nope "accepted tasks: $ACC"
one_task 2
tickn
ST=$(td3 build status --root "$R3" --json | jq -r '.state'); ACC=$(td3 build status --root "$R3" --json | jq '[.tasks[] | select(.accepted)] | length')
[ "$ACC" -eq 2 ] && [ "$ST" = finished ] && ok "both tasks are accepted and the run is finished" || nope "state $ST, accepted $ACC"
NOTICES=$(cut -f1 "$FAKE/prompts" | grep -c '^planner$')
[ "$NOTICES" -eq 1 ] && tail -1 "$FAKE/prompts" | grep -q 'is finished' && ok "the planner hears once, at the end" || nope "planner notices: $NOTICES"
tickn
[ "$(cut -f1 "$FAKE/prompts" | grep -c '^planner$')" -eq 1 ] && ok "and never again" || nope "the notice repeated"

[ "$(wc -l < "$FAKE/resets" 2>/dev/null | tr -d ' ')" = 2 ] && ok "each task went to a freshly reset conversation (two resets for two tasks)" || nope "resets: $(cat "$FAKE/resets" 2>/dev/null | tr '\n' ' ')"

printf '\n== a finished run is archived, and the checkout takes the next durable run ==\n'
OPEN=$(td3 build status --root "$R3" --json | jq '[.tasks[].attempts[]?] | length')
OUT=$(td3 state archive --run "$RUN3" --root "$R3" 2>"$TMP/err"); rc=$?
[ "$rc" -eq 0 ] && [ "$(printf '%s' "$OUT" | jq -r .state)" = archived ] && ok "the finished run archives ($OPEN attempts, every builder job ended)" || nope "archive rc $rc: $OUT $(cat "$TMP/err")"
[ ! -e "$R3/.tandem-relay/active-run.json" ] && ok "its marker is gone from the relay" || nope "the marker is still active"
[ -f "$R3/.tandem-relay/active-run.$RUN3.archived.json" ] && ok "and is kept aside under the run's own name" || nope "no run-named marker: $(ls "$R3/.tandem-relay")"
OUT=$(td3 build start --plan "$R3/plan.md" --root "$R3" --builder builder --planner planner --builder-kind codex --reviewer reviewer --reviewer-kind claude --check true 2>"$TMP/err"); rc=$?
NEXT=$(printf '%s' "$OUT" | jq -r .run_id 2>/dev/null)
[ "$rc" -eq 0 ] && [[ "$NEXT" =~ ^[0-9a-f]{32}$ ]] && [ "$NEXT" != "$RUN3" ] && ok "a second durable run starts in the same checkout" || nope "second start rc $rc: $OUT $(cat "$TMP/err")"

printf '\n== a builder that quit pauses the run when the tick observes agents ==\n'
R4="$TMP/repo4"; cp -R "$REPO" "$R4"; find "$R4/.tandem-relay" -mindepth 1 ! -name builder.json -delete 2>/dev/null
: > "$FAKE/prompts"; fake_set_state working; rm -f "$FAKE/absent"
td4() { env PATH="$TMP/bin:/usr/bin:/bin" FAKE="$FAKE" REPO="$R4" TANDEM_BUILD_ENGINE=durable TANDEM_STATE_HOME="$TMP/state4" tandem "$@"; }
td4 build start --plan "$R4/plan.md" --root "$R4" --builder builder --planner planner --builder-kind codex --reviewer reviewer --reviewer-kind claude --check true >/dev/null 2>&1
td4 build tick --root "$R4" --observe-agents >/dev/null 2>"$TMP/err"
ST=$(td4 build status --root "$R4" --json | jq -r '.state')
[ "$ST" = running ] && ok "a live builder leaves the run running" || nope "state $ST $(cat "$TMP/err")"
printf 'builder\n' > "$FAKE/absent"
td4 build tick --root "$R4" >/dev/null 2>&1
ST=$(td4 build status --root "$R4" --json | jq -r '.state')
[ "$ST" != paused ] && ok "a plain tick does not look for a gone agent" || nope "plain tick paused the run"
OUT=$(td4 build tick --root "$R4" --observe-agents 2>"$TMP/err")
[ "$(printf '%s' "$OUT" | jq -r .pause_code)" = agent_exited ] && ok "the observing tick pauses the run as agent_exited" || nope "tick out: $OUT $(cat "$TMP/err")"
ST=$(td4 build status --root "$R4" --json)
[ "$(printf '%s' "$ST" | jq -r .state)" = paused ] && printf '%s' "$ST" | jq -r .pause_reason | grep -q 'tandem-pair' \
  && ok "status says paused, with the way back" || nope "status: $ST"
td4 build recover --root "$R4" >/dev/null 2>&1
ST=$(td4 build status --root "$R4" --json | jq -r .state)
[ "$ST" = paused ] && ok "recover leaves the run paused while the builder is still gone" || nope "state after recover with builder gone: $ST"
rm -f "$FAKE/absent"
td4 build recover --root "$R4" >/dev/null 2>"$TMP/err"
ST=$(td4 build status --root "$R4" --json | jq -r .state)
[ "$ST" = running ] && ok "recover resumes it once the builder is back" || nope "state after recover: $ST $(cat "$TMP/err")"

printf '\n== tandem agent watch --root follows the run'"'"'s builder ==\n'
# The run in R4 is running again, with its builder back and working.
printf 'builder\n' > "$FAKE/absent"
OUT=$(td4 agent watch --root "$R4" --timeout 2000 2>"$TMP/err"); rc=$?
rm -f "$FAKE/absent"
[ "$rc" = 27 ] && [ "$OUT" = "$(printf 'agent_exited\tbuilder')" ] && ok "a run whose builder is gone exits 27 naming it" || nope "gone builder: rc=$rc out=$OUT $(cat "$TMP/err")"
# Flip the status until the watch returns, so the change cannot land before it
# subscribes. The reviewer is pinned so only the builder's status moves.
read -r _st _seq < "$FAKE/state"; fake_pin reviewer "$_st" "$_seq"
( while :; do fake_set_state blocked; sleep 0.1; fake_set_state working; sleep 0.1; done ) &
DRIVER=$!
OUT=$(td4 agent watch --root "$R4" --timeout 5000 2>"$TMP/err"); rc=$?
kill "$DRIVER" 2>/dev/null; wait "$DRIVER" 2>/dev/null
fake_set_state working; fake_unpin reviewer
case "$OUT" in "$(printf 'changed\tbuilder\t')"*) hit=1 ;; *) hit=0 ;; esac
[ "$rc" = 0 ] && [ "$hit" = 1 ] && ok "a status change of the run's builder prints changed" || nope "changed: rc=$rc out=$OUT $(cat "$TMP/err")"

printf '\n== the bridge wakes on events and files, not on a tick ==\n'
# With a 20 s backstop, a tick within 2 s of a change must come from the watch.
BLOG="$TMP/bridge.log"
env PATH="$TMP/bin:/usr/bin:/bin" FAKE="$FAKE" REPO="$R4" HERDR_ENV=1 TANDEM_BUILD_ENGINE=durable TANDEM_STATE_HOME="$TMP/state4" \
  HERDR_BRIDGE_BACKSTOP_SECONDS=20 HERDR_BRIDGE_PROMPT_TIMEOUT_MS=3000 "$ROOT_DIR/bin/tandem-bridge" --mailbox "$R4/.tandem-relay" --planner planner >"$BLOG" 2>&1 &
BRIDGE_PID=$!; DRIVER=""
stop_bridge() {
  [ -n "$DRIVER" ] && { kill "$DRIVER" 2>/dev/null; wait "$DRIVER" 2>/dev/null; DRIVER=""; }
  [ -n "${BRIDGE_PID:-}" ] || return 0
  pkill -P "$BRIDGE_PID" 2>/dev/null; kill "$BRIDGE_PID" 2>/dev/null; wait "$BRIDGE_PID" 2>/dev/null; BRIDGE_PID=""
}
trap 'stop_bridge; rm -rf "$TMP"' EXIT
ticks() { grep -c 'tandem-bridge: build tick' "$BLOG" 2>/dev/null || true; }
i=0; while [ "$(ticks)" -lt 1 ] && [ "$i" -lt 200 ]; do sleep 0.05; i=$((i+1)); done
N=$(ticks)
if [ "$N" -lt 1 ]; then
  nope "the bridge never ticked: $(tail -5 "$BLOG" | tr '\n' '|')"
else
  ( while :; do fake_set_state blocked; sleep 0.1; fake_set_state working; sleep 0.1; done ) &
  DRIVER=$!
  i=0; while [ "$(ticks)" -le "$N" ] && [ "$i" -lt 40 ]; do sleep 0.05; i=$((i+1)); done
  [ "$(ticks)" -gt "$N" ] && ok "the next tick follows a status change within 2 s, under a 20 s backstop" \
    || nope "no tick within 2 s of the status change: $(tail -5 "$BLOG" | tr '\n' '|')"
  kill "$DRIVER" 2>/dev/null; wait "$DRIVER" 2>/dev/null; DRIVER=""
  fake_set_state working
  # Let the last status flips drain: wait until no tick lands for 1 s.
  settle() {
    local n i=0
    while [ "$i" -lt 10 ]; do
      n=$(ticks); sleep 1
      [ "$(ticks)" -eq "$n" ] && return 0
      i=$((i+1))
    done
    return 1
  }
  settle || nope "the bridge never went quiet: $(tail -5 "$BLOG" | tr '\n' '|')"

  N=$(ticks); sleep 3
  [ "$(ticks)" -eq "$N" ] && ok "an idle run logs no tick for 3 s under a 20 s backstop" \
    || nope "an idle run ticked: $(tail -5 "$BLOG" | tr '\n' '|')"

  # A planner command writes the run's state; its nudge wakes the bridge.
  N=$(ticks)
  td4 build stop --root "$R4" >/dev/null 2>&1
  i=0; while [ "$(ticks)" -le "$N" ] && [ "$i" -lt 40 ]; do sleep 0.05; i=$((i+1)); done
  [ "$(ticks)" -gt "$N" ] && ok "a build stop from the planner is ticked within 2 s, under a 20 s backstop" \
    || nope "no tick within 2 s of build stop: $(tail -5 "$BLOG" | tr '\n' '|')"

  # A question dropped as tandem-ask drops it is picked up at once. Let the stop
  # notice's pass finish first, or that pass would find the question itself.
  settle || nope "the bridge never went quiet after the stop: $(tail -5 "$BLOG" | tr '\n' '|')"
  QD="$R4/.tandem-relay/q"
  printf '{"id":"e2e-q1","from":"builder","question":"ping?"}\n' > "$QD/e2e-q1.json.tmp"
  mv "$QD/e2e-q1.json.tmp" "$QD/e2e-q1.json"
  i=0; while ! grep -q 'delivering e2e-q1' "$BLOG" && [ "$i" -lt 40 ]; do sleep 0.05; i=$((i+1)); done
  grep -q 'delivering e2e-q1' "$BLOG" && ok "a queued question is picked up within 2 s, under a 20 s backstop" \
    || nope "the question was not picked up within 2 s: $(tail -5 "$BLOG" | tr '\n' '|')"
  grep -q 'woke: queued' "$BLOG" && ok "the bridge log says it woke for a queued message" \
    || nope "no 'woke: queued' line: $(tail -5 "$BLOG" | tr '\n' '|')"
fi
stop_bridge
fake_set_state working

printf '\n== a builder with no verified reset is not sent a task unless the run accepts that ==\n'
for mode in strict shared; do
  R="$TMP/repo-$mode"; cp -R "$REPO" "$R"; find "$R/.tandem-relay" -mindepth 1 ! -name builder.json -delete 2>/dev/null
  write_builder "$R/.tandem-relay" opencode
  : > "$FAKE/prompts"; rm -f "$FAKE/resets"; fake_set_state idle
  tdx() { env PATH="$TMP/bin:/usr/bin:/bin" FAKE="$FAKE" REPO="$R" TANDEM_BUILD_ENGINE=durable TANDEM_STATE_HOME="$TMP/state-$mode" tandem "$@"; }
  extra=(); [ "$mode" = shared ] && extra=(--share-context)
  tdx build start --plan "$R/plan.md" --root "$R" --builder builder --builder-kind opencode --reviewer reviewer --reviewer-kind claude --check true ${extra[@]+"${extra[@]}"} >/dev/null 2>&1
  tdx build tick --root "$R" >"$TMP/out" 2>"$TMP/err"
  if [ "$mode" = strict ]; then
    [ "$(prompts)" -eq 0 ] && ok "opencode has no verified reset: nothing is typed into its unknown conversation" || nope "typed into an unverified conversation"
    [ ! -s "$FAKE/resets" ] && ok "and no reset command is guessed at" || nope "a reset was typed"
  else
    [ "$(prompts)" -eq 1 ] && ok "with --share-context the same builder is sent its task, once" || nope "shared: prompts $(prompts) $(cat "$TMP/err")"
  fi
done

printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

#!/usr/bin/env bash
# A paused run always has a way out, through the shipped binary and a fake Herdr.
#
# No model is called. A builder that never acknowledged its prompt leaves the run with
# an open delivery, which only its nonce or a person's statement can end:
#   - the archive refuses and prints the command;
#   - that command refuses while the pane is working or the stop holds the writer;
#   - with the pane idle it ends the delivery, types nothing, and a new run starts.
# A gone builder pane holds a stop until the agent is back, and tandem-pair is
# what brings it back.
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
printf '# Plan\n\n**Goal:** g.\n\n### Task 1: First\n\nfirst thing\n' > "$REPO/plan.md"
git -C "$REPO" add . && git -C "$REPO" commit -qm plan
td() { env PATH="$TMP/bin:/usr/bin:/bin" FAKE="$FAKE" REPO="$REPO" TANDEM_BUILD_ENGINE=durable TANDEM_STATE_HOME="$TMP/state" tandem "$@"; }
prompts() { [ -f "$FAKE/prompts" ] && wc -l < "$FAKE/prompts" | tr -d ' ' || echo 0; }
start_run() {
  td build start --plan "$REPO/plan.md" --root "$REPO" --builder builder --planner planner --builder-kind codex --reviewer reviewer --reviewer-kind claude --check true 2>"$TMP/err" | jq -r .run_id
}
state() { td build status --root "$REPO" --json | jq -r '.state + "/" + (.pause_code // "")'; }
wait_paused() { td build wait --root "$REPO" --until paused --tick --timeout 1500 >/dev/null 2>"$TMP/err"; }
# The stop notice typed into the pane starts a turn. Ending that turn is what the
# builder does by itself.
turn_ends() { td build tick --root "$REPO" >/dev/null 2>&1; fake_set_state idle; }

printf '\n== a prompt no builder acknowledged ==\n'
fake_set_state idle
RUN=$(start_run)
td build tick --root "$REPO" >/dev/null 2>&1
[ "$(prompts)" -eq 1 ] && ok "the task prompt is typed" || nope "prompts: $(prompts)"
fake_set_state idle
EFFECT=$(td state export --run "$RUN" --output "$TMP/export" --root "$REPO" | jq -r '.unresolved[0]' | cut -d' ' -f2)
[[ "$EFFECT" =~ ^[0-9a-f]{32}$ ]] && ok "the export names the open delivery" || nope "effect: $EFFECT"

SETTLE=(state settle-delivery --run "$RUN" --effect "$EFFECT" --abandon --because "the pane never showed it" --root "$REPO")
td "${SETTLE[@]}" >/dev/null 2>"$TMP/err"; rc=$?
[ "$rc" -eq 23 ] && grep -q 'tandem build stop' "$TMP/err" && ok "a running run refuses a settle and says to stop" || nope "rc $rc: $(cat "$TMP/err")"

printf '\n== stop, wait, recover, archive, settle, archive, start ==\n'
td build stop --root "$REPO" >/dev/null 2>&1
turn_ends
wait_paused && ok "the wait ticks the stop to paused" || nope "wait: $(state) $(cat "$TMP/err")"
[ "$(state)" = paused/stopped ] && ok "the run is stopped" || nope "state: $(state)"
td state recover --run "$RUN" --root "$REPO" >/dev/null 2>"$TMP/err" && ok "recover goes through" || nope "recover: $(cat "$TMP/err")"
td state archive --run "$RUN" --root "$REPO" >/dev/null 2>"$TMP/err"; rc=$?
[ "$rc" -eq 23 ] && grep -qF "tandem state settle-delivery --run $RUN --effect $EFFECT --abandon" "$TMP/err" \
  && ok "the archive refuses and prints the command" || nope "archive rc $rc: $(cat "$TMP/err")"

fake_set_state working
td "${SETTLE[@]}" >/dev/null 2>"$TMP/err"; rc=$?
[ "$rc" -eq 23 ] && grep -q 'working' "$TMP/err" && ok "a working pane refuses the settle" || nope "working: rc $rc: $(cat "$TMP/err")"
fake_set_state blocked
td "${SETTLE[@]}" >/dev/null 2>"$TMP/err"; rc=$?
[ "$rc" -eq 23 ] && ok "a pane at a dialog refuses it too" || nope "blocked: rc $rc: $(cat "$TMP/err")"

fake_set_state idle
sent=$(prompts)
OUT=$(td "${SETTLE[@]}" 2>"$TMP/err"); rc=$?
[ "$rc" -eq 0 ] && [ "$(printf '%s' "$OUT" | jq -r .state)" = cancelled ] && ok "an idle pane lets the statement through" || nope "settle rc $rc: $OUT $(cat "$TMP/err")"
td state recover --run "$RUN" --root "$REPO" >/dev/null 2>&1
[ "$(prompts)" -eq "$sent" ] && ok "nothing is typed again" || nope "typed $(( $(prompts) - sent )) more"
td state archive --run "$RUN" --root "$REPO" >/dev/null 2>"$TMP/err" && ok "the archive goes through" || nope "archive: $(cat "$TMP/err")"

RUN2=$(start_run)
[[ "$RUN2" =~ ^[0-9a-f]{32}$ ]] && [ "$RUN2" != "$RUN" ] && ok "a new run starts in the same checkout" || nope "start: $RUN2 $(cat "$TMP/err")"
sent=$(prompts)
td build tick --root "$REPO" >/dev/null 2>&1
[ "$(prompts)" -eq $(( sent + 1 )) ] && ok "and types its first prompt" || nope "prompts: $(prompts), was $sent"

printf '\n== a gone builder pane holds the stop until the agent is back ==\n'
printf 'builder\n' > "$FAKE/absent"
td build stop --root "$REPO" >/dev/null 2>&1
wait_paused; rc=$?
[ "$rc" -eq 23 ] && [ "$(state)" = stopping/stopping ] && ok "the wait times out with the run stopping" || nope "wait rc $rc, state $(state)"
EFFECT2=$(td state export --run "$RUN2" --output "$TMP/export2" --root "$REPO" | jq -r '.unresolved[0]' | cut -d' ' -f2)
td state settle-delivery --run "$RUN2" --effect "$EFFECT2" --abandon --because gone --root "$REPO" >/dev/null 2>"$TMP/err"; rc=$?
[ "$rc" -eq 23 ] && grep -q 'writer lease' "$TMP/err" && ok "settling cannot end it, because the stop holds the writer" || nope "settle rc $rc: $(cat "$TMP/err")"
rm -f "$FAKE/absent"; fake_set_state idle
turn_ends
wait_paused && [ "$(state)" = paused/stopped ] && ok "with the agent back the same wait finishes the stop" || nope "state $(state) $(cat "$TMP/err")"
td state settle-delivery --run "$RUN2" --effect "$EFFECT2" --abandon --because gone --root "$REPO" >/dev/null 2>"$TMP/err" && ok "and then the prompt can be settled" || nope "settle: $(cat "$TMP/err")"
td state recover --run "$RUN2" --root "$REPO" >/dev/null 2>&1
td state archive --run "$RUN2" --root "$REPO" >/dev/null 2>"$TMP/err" && ok "and the run archived" || nope "archive: $(cat "$TMP/err")"

printf '\n== tandem-pair brings a gone builder back on a stopping run ==\n'
# The fake keeps one agent's state and knows nothing of opening panes, so this
# wrapper answers those commands and starting the builder makes it present again.
mv "$TMP/bin/herdr" "$TMP/bin/herdr.fake"
cat > "$TMP/bin/herdr" <<STUB
#!/usr/bin/env bash
printf '%s\\n' "\$*" >> "$FAKE/pair.log"
case "\$1 \$2" in
  "pane split") printf '{"result":{"pane":{"pane_id":"w1:builder"}}}\\n'; exit 0 ;;
  "pane run"|"pane rename"|"pane title"|"agent rename"|"tab rename"|"tab set-title") printf '{"result":{}}\\n'; exit 0 ;;
  "agent start") rm -f "$FAKE/absent"; printf '{"result":{"agent":{"name":"builder"}}}\\n'; exit 0 ;;
  # tandem-pair looks for a Claude planner, and the fake has one kind.
  "agent list") "$TMP/bin/herdr.fake" "\$@" | sed 's/"agent":"codex"\\(,[^}]*"name":"planner"\\)/"agent":"claude"\\1/'; exit 0 ;;
esac
exec "$TMP/bin/herdr.fake" "\$@"
STUB
chmod +x "$TMP/bin/herdr"
# pair [ENV=VALUE ...]: CODEX_HOME is the test's own, because pairing records
# the repo in Codex's trusted projects.
pair() {
  (cd "$REPO" && env PATH="$TMP/bin:$ROOT_DIR/bin:/usr/bin:/bin" FAKE="$FAKE" REPO="$REPO" TANDEM_STATE_HOME="$TMP/state" HERDR_ENV=1 HERDR_PANE_ID=w1:planner \
    CODEX_HOME="$TMP/codex" HERDR_PAIR_START_TIMEOUT_MS=1000 "$@" "$ROOT_DIR/bin/tandem-pair" --skip-doctor --builder codex --cwd "$REPO" 2>&1)
}
stop_bridge() {
  BP=$(cat "$REPO/.tandem-relay/bridge.pid" 2>/dev/null); [ -z "$BP" ] || { kill "$BP" 2>/dev/null; while kill -0 "$BP" 2>/dev/null; do sleep 0.1; done; }
}
RUN3=$(start_run)
[[ "$RUN3" =~ ^[0-9a-f]{32}$ ]] || nope "start: $RUN3 $(cat "$TMP/err")"
td build tick --root "$REPO" >/dev/null 2>&1
fake_set_state idle
printf 'builder\n' > "$FAKE/absent"
td build stop --root "$REPO" >/dev/null 2>&1
wait_paused; rc=$?
[ "$rc" -eq 23 ] && [ "$(state)" = stopping/stopping ] && ok "the stop holds with the pane gone" || nope "wait rc $rc, state $(state)"
OUT=$(pair HERDR_SKIP_HOOK_CHECK=1); rc=$?
[ "$rc" -eq 0 ] && ok "tandem-pair runs on the stopping run" || nope "pair rc $rc: $OUT"
grep -qF "$REPO" "$TMP/codex/config.toml" 2>/dev/null && ok "it records Codex's trust in the test's own home" || nope "no trust entry in $TMP/codex/config.toml"
case "$(cat "$FAKE/pair.log")" in *"agent start builder"*) ok "it starts the builder again" ;; *) nope "pair.log: $(cat "$FAKE/pair.log")" ;; esac
[ ! -e "$FAKE/absent" ] && ok "and the pane is back" || nope "still absent"
# tandem-pair started the bridge. It would type notes into the fake's one shared
# state, so it stops here and the test ticks by itself.
stop_bridge
fake_set_state idle
wait_paused
if [ "$(state)" != paused/stopped ]; then turn_ends; wait_paused; fi
[ "$(state)" = paused/stopped ] && ok "the wait then finishes the stop" || nope "state $(state) $(cat "$TMP/err")"
fake_set_state idle
EFFECT3=$(td state export --run "$RUN3" --output "$TMP/export3" --root "$REPO" | jq -r '.unresolved[0]' | cut -d' ' -f2)
td state settle-delivery --run "$RUN3" --effect "$EFFECT3" --abandon --because gone --root "$REPO" >/dev/null 2>"$TMP/err" && ok "the prompt can be given up on" || nope "settle: $(cat "$TMP/err")"
td state recover --run "$RUN3" --root "$REPO" >/dev/null 2>&1
td state archive --run "$RUN3" --root "$REPO" >/dev/null 2>"$TMP/err" && ok "and the run reaches archived" || nope "archive: $(cat "$TMP/err")"

printf '\n== tandem-pair reads each pane before it calls the pair ready ==\n'
# Codex 0.167.0 opened on its hook review prompt, Herdr said idle, and the pair
# was called ready. The first prompt sent to the pane was lost (F29).
printf '  Hooks need review\n  6 hooks are new or changed.\n\n› 1. Review hooks\n  2. Trust all and continue\n\n  enter confirm · esc skip\n' > "$FAKE/pane"
OUT=$(pair); rc=$?; stop_bridge
[ "$rc" -eq 23 ] && ok "a builder at a dialog Herdr calls idle stops the pairing" || nope "pair rc $rc: $OUT"
case "$OUT" in *"the builder is not at its prompt"*"Hooks need review"*) ok "naming the dialog" ;; *) nope "the dialog is not named: $OUT" ;; esac
case "$OUT" in *"2. Trust all and continue"*"Answer it in pane w1:builder"*) ok "showing the screen and the pane to answer it in" ;; *) nope "no screen or pane: $OUT" ;; esac
case "$OUT" in *"pair ready"*) nope "and still calls the pair ready: $OUT" ;; *) ok "and the pair is not called ready" ;; esac
OUT=$(pair HERDR_SKIP_HOOK_CHECK=1); rc=$?; stop_bridge
case "$rc:$OUT" in 0:*"overridden"*"pair ready"*) ok "HERDR_SKIP_HOOK_CHECK=1 pairs anyway and says so" ;; *) nope "override rc $rc: $OUT" ;; esac
printf '› Ask Codex to do anything\n\n  ← for agents · ? for shortcuts\n' > "$FAKE/pane"
OUT=$(pair); rc=$?; stop_bridge
case "$rc:$OUT" in 0:*"pair ready"*) ok "with the dialog answered the same command pairs" ;; *) nope "answered rc $rc: $OUT" ;; esac

printf '\ntests/test-way-out-e2e: passed %d, failed %d\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

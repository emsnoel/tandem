# shellcheck shell=bash
# tests/lib/note-cases.sh: the harness the pause note case suites share. A real
# bin/tandem-bridge, the shipped tandem binary and a fake Herdr, with the
# planner's pane pinned idle. Sourced by tests/test-pause-note-cases.sh and
# tests/test-pause-note-quiet.sh after `set -uo pipefail`.
# shellcheck source=records.sh
. "$(cd "$(dirname "$0")" && pwd)/lib/records.sh"
ROOT_DIR=$(cd "$(dirname "$0")/.." && pwd)

PASS=0; FAIL=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }
has()  { case "$2" in *"$3"*) ok "$1" ;; *) nope "$1 (missing: $3): $2" ;; esac; }
lack() { case "$2" in *"$3"*) nope "$1 (found: $3): $2" ;; *) ok "$1" ;; esac; }

command -v jq >/dev/null || { echo "need jq" >&2; exit 2; }
TMP=$(mktemp -d); trap 'stop_driver; rm -rf "$TMP"' EXIT
(cd "$ROOT_DIR" && go build -o "$TMP/bin/tandem" ./cmd/tandem) || { echo "could not build tandem"; exit 1; }
ln -s "$(command -v jq)" "$TMP/bin/jq"
. "$ROOT_DIR/tests/lib/fake-herdr.sh"
. "$ROOT_DIR/tests/lib/fake-socket.sh"
FAKE="$TMP/fake"
fake_herdr_install "$TMP/bin" "$FAKE"
FAKE="$FAKE" PATH="$TMP/bin:$PATH" fake_socket_start "$TMP" || exit 1
# A started turn counts after one second, not eight. The fake logs every prompt
# under CODEX_HOME so a turn that ends at once can be told from one that worked.
export TANDEM_FAIL_WITHIN_MS=1000 CODEX_HOME="$TMP/codex"

BASE="$TMP/base"; mkdir -p "$BASE/.tandem-relay"; git -C "$BASE" init -q
git -C "$BASE" config user.email t@t; git -C "$BASE" config user.name t
printf '.tandem-relay/\n' > "$BASE/.gitignore"; write_builder "$BASE/.tandem-relay"
printf '# Plan\n\n**Goal:** g.\n\n### Task 1: First\n\nfirst thing\n' > "$BASE/plan.md"
git -C "$BASE" add . && git -C "$BASE" commit -qm plan

TAB=$(printf '\t'); DRIVER=""
stop_driver() { [ -z "$DRIVER" ] || { pkill -P "$DRIVER" 2>/dev/null; kill "$DRIVER" 2>/dev/null; wait "$DRIVER" 2>/dev/null; DRIVER=""; }; }
td() { env PATH="$TMP/bin:/usr/bin:/bin" FAKE="$FAKE" REPO="$R" TANDEM_BUILD_ENGINE=durable TANDEM_STATE_HOME="$TMP/state-$C" TANDEM_ACK_WINDOW_MS="${ACK_MS:-}" TANDEM_SILENCE_WINDOW_MS="${SILENCE_MS:-}" tandem "$@"; }
# pass [N]: N bridge passes. Each one ticks the run and tells the planner of a new pause.
pass() {
  local i
  for ((i = 0; i < ${1:-1}; i++)); do
    env PATH="$TMP/bin:$ROOT_DIR/bin:/usr/bin:/bin" FAKE="$FAKE" REPO="$R" HERDR_ENV=1 TANDEM_BUILD_ENGINE=durable \
      TANDEM_STATE_HOME="$TMP/state-$C" TANDEM_ACK_WINDOW_MS="${ACK_MS:-}" TANDEM_BUILDER_ACK_GRACE_MS="${GRACE_MS:-}" HERDR_BRIDGE_PROMPT_TIMEOUT_MS=3000 \
      "$ROOT_DIR/bin/tandem-bridge" --mailbox "$R/.tandem-relay" --planner planner --once >>"$TMP/bridge.log" 2>&1
  done
}
state() { td build status --root "$R" --json | jq -r '.state + "/" + (.pause_code // "")'; }
sent_to() { local n; n=$(grep -c "^$1$TAB" "$FAKE/prompts" 2>/dev/null); echo "${n:-0}"; }
last_note() {
  local n; n=$(grep -n "^planner$TAB" "$FAKE/prompts" | tail -1 | cut -d: -f1)
  [ -z "$n" ] || cat "$FAKE/prompt.$n.txt"
}
# new_case NAME CHECK: a fresh checkout with a run that has not been ticked.
new_case() {
  C=$1; R="$TMP/$1"; cp -R "$BASE" "$R"
  rm -rf "$FAKE/prompts" "$FAKE"/prompt.*.txt "$FAKE/resets" "$FAKE/pane" "$FAKE/swallow" "$CODEX_HOME"
  fake_set_state idle; fake_pin planner idle 10
  td build start --plan "$R/plan.md" --root "$R" --builder builder --planner planner --builder-kind codex \
    --reviewer reviewer --reviewer-kind claude --check "$2" >/dev/null 2>"$TMP/err" || nope "$1: start failed: $(cat "$TMP/err")"
}
# once_only: another pass does not repeat the note.
once_only() { local before; before=$(sent_to planner); pass 2; [ "$(sent_to planner)" = "$before" ] && ok "$1: and never again" || nope "$1: the note was repeated"; }
field() { printf '%s' "$1" | grep -o -- "--$2 [0-9a-f]*" | head -1 | cut -d' ' -f2; }
# bridge_up: one bridge that stays up in the background, as a real pair has.
bridge_up() {
  env PATH="$TMP/bin:$ROOT_DIR/bin:/usr/bin:/bin" FAKE="$FAKE" REPO="$R" HERDR_ENV=1 TANDEM_BUILD_ENGINE=durable TANDEM_STATE_HOME="$TMP/state-$C" \
    TANDEM_SILENCE_WINDOW_MS="$SILENCE_MS" HERDR_BRIDGE_BACKSTOP_SECONDS=1 HERDR_BRIDGE_PROMPT_TIMEOUT_MS=3000 \
    "$ROOT_DIR/bin/tandem-bridge" --mailbox "$R/.tandem-relay" --planner planner >>"$TMP/bridge.log" 2>&1 &
  DRIVER=$!
}
# builder_turn: the builder reads its prompt, works, and hands in a report.
builder_turn() {
  local pl att job eff nonce
  pass 4
  pl=$(cat "$FAKE/prompt.1.txt"); att=$(field "$pl" attempt); job=$(field "$pl" job); eff=$(field "$pl" effect); nonce=$(field "$pl" nonce)
  [ -n "$att" ] && [ -n "$nonce" ] || { nope "$C: no identity in the task prompt"; return 1; }
  td build event new --root "$R" --kind builder_started --task 1 --attempt "$att" --job "$job" --effect "$eff" --nonce "$nonce" >/dev/null 2>&1
  printf 'work\n' > "$R/file1.txt"; printf 'done\n' > "$TMP/report.md"
  td build event new --root "$R" --kind builder_submitted --task 1 --attempt "$att" --job "$job" --effect "$eff" --nonce "$nonce" --payload "$TMP/report.md" >/dev/null 2>&1
  fake_set_state idle
  pass 4
}
# start_turn: one pass types the task, and the builder acknowledges it. IDENT
# holds the prompt's ids for later events.
start_turn() {
  local pl
  pass
  pl=$(cat "$FAKE/prompt.1.txt")
  IDENT=(--task 1 --attempt "$(field "$pl" attempt)" --job "$(field "$pl" job)" --effect "$(field "$pl" effect)" --nonce "$(field "$pl" nonce)")
  td build event new --root "$R" --kind builder_started "${IDENT[@]}" >/dev/null 2>&1
}

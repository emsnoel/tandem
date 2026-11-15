# shellcheck shell=bash
# tests/lib/pair-worktrees.sh: the shared setup of the suites that pair several
# checkouts through the shipped scripts and a fake Herdr that holds several
# named agents. Sourced after ROOT_DIR is set. It builds the branch's tandem,
# installs the fakes in TMP/bin, and defines the helpers below. FAKE/agents is
# the fake's pane table (tests/lib/fake-herdr-panes.sh); each suite writes it
# and then calls fake_socket_start.

PASS=0; FAIL=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }

TMP=$(mktemp -d)
# cleanup: stop every bridge a pair under TMP started, then remove TMP. A suite
# may leave a folder read-only, so write access comes back first.
cleanup() {
  local f p
  for f in "$TMP"/*/.tandem-relay/bridge.pid "$TMP"/*/*/.tandem-relay/bridge.pid; do
    p=$(cat "$f" 2>/dev/null) || continue
    pkill -P "$p" 2>/dev/null; kill "$p" 2>/dev/null
  done
  chmod -R u+w "$TMP" 2>/dev/null
  rm -rf "$TMP"
}
trap cleanup EXIT
# The branch's own tandem, ahead of any installed one.
(cd "$ROOT_DIR" && go build -o "$TMP/bin/tandem" ./cmd/tandem) || { echo "could not build tandem"; exit 1; }

. "$ROOT_DIR/tests/lib/fake-herdr.sh"
. "$ROOT_DIR/tests/lib/fake-socket.sh"
FAKE="$TMP/fake"
fake_herdr_install "$TMP/bin" "$FAKE"
mv "$TMP/bin/herdr" "$TMP/bin/herdr.fake"
printf '› Ask Codex to do anything\n\n  ← for agents · ? for shortcuts\n' > "$FAKE/pane"
. "$ROOT_DIR/tests/lib/fake-herdr-panes.sh"
fake_herdr_panes_install "$TMP/bin"

# as_pane DIR PANE CMD...: run CMD in DIR as the shell in PANE, with every home the
# test's own.
as_pane() {
  local dir="$1" pane="$2"; shift 2
  (cd "$dir" && env PATH="$TMP/bin:$ROOT_DIR/bin:/usr/bin:/bin" HOME="$TMP/home" FAKE="$FAKE" REPO="$dir" \
    HERDR_ENV=1 HERDR_PANE_ID="$pane" HERDR_SOCKET_PATH="$HERDR_SOCKET_PATH" \
    TANDEM_STATE_HOME="$TMP/state" HERDR_TANDEM_CONFIG="$TMP/tandem.json" CODEX_HOME="$TMP/codex" \
    COPILOT_HOME="$TMP/copilot" TANDEM_SKILL_HOMES="$TMP/skills" TANDEM_SHARE_DIR="$TMP/share" \
    HERDR_CONFIG_PATH="$TMP/herdr/config.toml" HERDR_PAIR_START_TIMEOUT_MS=1000 HERDR_SKIP_HOOK_CHECK=1 \
    TANDEM_FAIL_WITHIN_MS=1000 HERDR_BRIDGE_BACKSTOP_SECONDS=1 "$@" 2>&1)
}
# pair DIR PANE [ARG...]: run tandem-pair for the checkout DIR from PANE.
pair() {
  local dir="$1" pane="$2"; shift 2
  as_pane "$dir" "$pane" "$ROOT_DIR/bin/tandem-pair" --skip-doctor --approve-kind codex --cwd "$dir" "$@"
}
# pane_of NAME: the pane holding agent NAME.
pane_of() { awk -F'\t' -v n="$1" '$2 == n { print $1; exit }' "$FAKE/agents"; }
names_in() { awk -F'\t' -v t="$1" '$5 == t && $2 != "-" { print $2 }' "$FAKE/agents" | sort | tr '\n' ' '; }
# last_to: the agent the latest prompt went to.
last_to() { tail -1 "$FAKE/prompts" 2>/dev/null | cut -f1; }
prompts() { [ -f "$FAKE/prompts" ] && wc -l < "$FAKE/prompts" | tr -d ' ' || echo 0; }
bridge_of() { cat "$1/.tandem-relay/bridge.pid" 2>/dev/null; }
record_of() { cat "$1/.tandem-relay/pair.json" 2>/dev/null; }

# exits_within PID: the process ends within 5 s. A killed bridge exits once its
# event wait returns, which the 1 s backstop above bounds.
exits_within() { local _; for _ in $(seq 1 50); do kill -0 "$1" 2>/dev/null || return 0; sleep 0.1; done; return 1; }

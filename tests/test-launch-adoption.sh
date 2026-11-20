#!/usr/bin/env bash
# tandem-pair adopts a running builder only beside the planner, says what it
# cannot enforce on one, and gives a builder launch no network scope.
#
# Hermetic: herdr is stubbed and CODEX_HOME is a temp dir.
set -uo pipefail
. "$(cd "$(dirname "$0")" && pwd)/lib/records.sh"
ROOT=$(cd "$(dirname "$0")/.." && pwd)
PAIR="${1:-$ROOT/bin/tandem-pair}"
. "$ROOT/tests/lib/launch-fixture.sh"

echo "--- adoption reports what it cannot enforce ---"
cat > "$TMP/bin/herdr" <<'STUB2'
#!/usr/bin/env bash
case "$1 $2" in
  "agent list")  printf '{"result":{"agents":[{"pane_id":"p1","agent":"claude","name":null,"cwd":"%s","agent_status":"idle"},{"pane_id":"p9","agent":"copilot","name":null,"cwd":"%s","agent_status":"idle"}]}}' "$FAKE_CWD" "$FAKE_CWD" ;;
  "pane layout") printf '{"result":{"layout":{"panes":[{"pane_id":"p1","rect":{"width":200,"height":50}}]}}}' ;;
  *) printf '{"error":{"code":"unexpected","message":"stub got: %s"}}' "$*" >&2; exit 1 ;;
esac
STUB2
chmod +x "$TMP/bin/herdr"
run_pair env HERDR_BUILDER_KIND=codex HERDR_BUILDER_MODEL=gpt-5.4
chk  "adopts the running agent whatever its kind" "adopting the running copilot agent"
chk  "names the axes it cannot enforce"           "keeps the settings it started with"
chk  "says how to apply them"                     "Restart its pane"
nchk "never adopts the planner pane"              "adopting the running claude agent"

echo "--- adoption stays beside the planner ---"
# A claude session in another tab is someone else's work. It must not become
# the builder, and --builder KIND must not adopt an agent of another kind.
cat > "$TMP/bin/herdr" <<'STUB3'
#!/usr/bin/env bash
case "$1 $2" in
  "agent list")  printf '{"result":{"agents":[{"pane_id":"p1","tab_id":"t1","agent":"claude","name":null,"cwd":"%s","agent_status":"idle"},{"pane_id":"p9","tab_id":"%s","agent":"%s","name":null,"cwd":"%s","agent_status":"idle"}]}}' "$FAKE_CWD" "$FAKE_TAB" "$FAKE_KIND" "$FAKE_CWD" ;;
  "pane layout") printf '{"result":{"layout":{"panes":[{"pane_id":"p1","rect":{"width":200,"height":50}}]}}}' ;;
  *) printf '{"error":{"code":"unexpected","message":"stub got: %s"}}' "$*" >&2; exit 1 ;;
esac
STUB3
chmod +x "$TMP/bin/herdr"
run_pair env FAKE_TAB=t2 FAKE_KIND=claude
nchk "does not adopt an agent in another tab"         "adopting the running"
run_pair env FAKE_TAB=t1 FAKE_KIND=claude -- --builder codex
nchk "--builder codex does not adopt a claude agent"  "adopting the running"
run_pair env FAKE_TAB=t1 FAKE_KIND=codex -- --builder codex
chk  "--builder codex adopts a codex agent beside the planner" "adopting the running codex agent"

echo "--- a builder launch has no network scope ---"
mkdir -p "$TMP/judgebin"
cat > "$TMP/judgebin/codex" <<STUB
#!/usr/bin/env bash
case "\$1" in
  sandbox)   printf '%s\n' "\$*" >> "$TMP/codex-sandbox.log"; exit 0 ;;
  --version) echo "codex-cli 0.155.1" ;;
  *)         exit 1 ;;
esac
STUB
chmod +x "$TMP/judgebin/codex"
JPATH="$TMP/judgebin:$TMP/bin:$PATH"
# Back to the planner-only session: the adoption cases left a running builder,
# which is never launched.
cat > "$TMP/bin/herdr" <<'STUB'
#!/usr/bin/env bash
case "$1 $2" in
  "agent list")
    # FAKE_RUNNING="name:kind ..." adds already-running agents beside the planner.
    printf '{"result":{"agents":[{"pane_id":"p1","agent":"claude","name":null,"cwd":"%s","agent_status":"idle"}' "$FAKE_CWD"
    for _a in ${FAKE_RUNNING:-}; do
      printf ',{"pane_id":"r-%s","agent":"%s","name":"%s","cwd":"%s","agent_status":"idle"}' "${_a%%:*}" "${_a##*:}" "${_a%%:*}" "$FAKE_CWD"
    done
    printf ']}}' ;;
  "pane layout") printf '{"result":{"layout":{"panes":[{"pane_id":"p1","rect":{"width":200,"height":50}}]}}}' ;;
  *) printf '{"error":{"code":"unexpected","message":"stub got: %s"}}' "$*" >&2; exit 1 ;;
esac
STUB
chmod +x "$TMP/bin/herdr"

run_pair env PATH="$JPATH"
chk  "it keeps the ordinary sandbox"         "--sandbox workspace-write"
nchk "it adds no permissions profile"        "permissions.tandem-builder"
nchk "it sets no builder environment"         "shell_environment_policy"
nchk "the builder record has no network field"    '"network_allow"'
[ ! -s "$TMP/codex-sandbox.log" ] && ok "a dry run never probes the network" \
  || nope "a dry run probed the network: $(cat "$TMP/codex-sandbox.log")"

# An adopted builder has the same identity format, with no network field.
cat > "$TMP/bin/herdr" <<'STUB'
#!/usr/bin/env bash
case "$1 $2" in
  "agent list")  printf '{"result":{"agents":[{"pane_id":"p1","agent":"claude","name":null,"cwd":"%s","agent_status":"idle"},{"pane_id":"p9","agent":"codex","name":null,"cwd":"%s","agent_status":"idle"}]}}' "$FAKE_CWD" "$FAKE_CWD" ;;
  "pane layout") printf '{"result":{"layout":{"panes":[{"pane_id":"p1","rect":{"width":200,"height":50}}]}}}' ;;
  *) printf '{"error":{"code":"unexpected","message":"stub got: %s"}}' "$*" >&2; exit 1 ;;
esac
STUB
chmod +x "$TMP/bin/herdr"
run_pair env PATH="$JPATH"
chk  "adopts the running codex builder"          "adopting the running codex agent"
nchk "an adopted builder's record has no network field" '"network_allow"'

echo; printf 'launch-adoption: passed %d, failed %d\n' "$PASS" "$FAIL"; [ "$FAIL" -eq 0 ]

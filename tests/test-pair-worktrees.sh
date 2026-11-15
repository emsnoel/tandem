#!/usr/bin/env bash
# Two worktrees of one repository each get their own pair, through the shipped
# scripts and a fake Herdr that holds several named agents.
#
# Herdr agent names are global, so each checkout's pair takes names from its
# checkout and records them in its relay (pair.json):
#   - the main checkout pairs as planner, builder and reviewer, and a linked
#     worktree wt-b as planner-wt-b, builder-wt-b and reviewer-wt-b;
#   - each checkout has its own pair.json and its own bridge;
#   - a verdict or a handback reaches only its own planner;
#   - after `git worktree move wt-b wt-c` the record still names planner-wt-b;
#   - tandem-unpair in that worktree closes only its own panes.
set -uo pipefail
ROOT_DIR=$(cd "$(dirname "$0")/.." && pwd)

PASS=0; FAIL=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }

TMP=$(mktemp -d)
cleanup() {
  local f p
  for f in "$TMP"/main/.tandem-relay/bridge.pid "$TMP"/wt-*/.tandem-relay/bridge.pid; do
    p=$(cat "$f" 2>/dev/null) || continue
    pkill -P "$p" 2>/dev/null; kill "$p" 2>/dev/null
  done
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

# The single-agent fake keeps the status and logs prompts. This wrapper keeps
# the panes: FAKE/agents has one line per pane, "pane<TAB>name<TAB>cwd<TAB>kind<TAB>tab",
# with "-" for no name.
cat > "$TMP/bin/herdr" <<'STUB'
#!/usr/bin/env bash
A="$FAKE/agents"
printf '%s\n' "$*" >> "$FAKE/calls"
# row FIELD VALUE: the first line whose FIELD (1 pane, 2 name) is VALUE.
row() { awk -F'\t' -v f="$1" -v v="$2" '$f == v { print; exit }' "$A"; }
# set_row PANE LINE: replace PANE's line, or drop it when LINE is empty.
set_row() {
  awk -F'\t' -v p="$1" -v l="$2" '$1 == p { if (l != "") print l; next } { print }' "$A" > "$A.$$" && mv -f "$A.$$" "$A"
}
# agent_json LINE: one agent object with the shared status.
agent_json() {
  local pane name cwd kind tab st seq sess n
  IFS=$'\t' read -r pane name cwd kind tab <<<"$1"
  read -r st seq < "$FAKE/state"; sess=$(cat "$FAKE/session")
  n="\"$name\""; [ "$name" = - ] && n=null
  printf '{"agent":"%s","agent_status":"%s","cwd":"%s","name":%s,"pane_id":"%s","tab_id":"%s","state_change_seq":%s,"agent_session":{"value":"%s"}}' \
    "$kind" "$st" "$cwd" "$n" "$pane" "$tab" "$seq" "$sess"
}
not_found() { printf '{"error":{"code":"agent_not_found","message":"agent target %s not found"}}\n' "$1" >&2; exit 1; }
case "$1 $2" in
  "agent list")
    sep=""; printf '{"id":"x","result":{"agents":['
    while IFS= read -r l; do printf '%s' "$sep"; agent_json "$l"; sep=","; done < "$A"
    printf '],"type":"agent_list"}}\n'; exit 0 ;;
  "agent get")
    l=$(row 2 "$3"); [ -n "$l" ] || l=$(row 1 "$3"); [ -n "$l" ] || not_found "$3"
    printf '{"id":"x","result":{"agent":%s,"type":"agent_info"}}\n' "$(agent_json "$l")"; exit 0 ;;
  "agent prompt"|"agent read"|"agent wait"|"agent send-keys")
    l=$(row 2 "$3"); [ -n "$l" ] || l=$(row 1 "$3"); [ -n "$l" ] || not_found "$3"
    exec "$(dirname "$0")/herdr.fake" "$@" ;;
  "agent rename")
    IFS=$'\t' read -r pane _ cwd kind tab <<<"$(row 1 "$3")"
    set_row "$3" "$(printf '%s\t%s\t%s\t%s\t%s' "$pane" "$4" "$cwd" "$kind" "$tab")"
    printf '{"result":{}}\n'; exit 0 ;;
  "agent start")
    name="$3"; shift 3; pane=""; kind=""
    while [ $# -gt 0 ]; do
      case "$1" in --pane) pane="$2"; shift 2 ;; --kind) kind="$2"; shift 2 ;; --) break ;; *) shift ;; esac
    done
    IFS=$'\t' read -r _ _ cwd _ tab <<<"$(row 1 "$pane")"
    set_row "$pane" "$(printf '%s\t%s\t%s\t%s\t%s' "$pane" "$name" "$cwd" "$kind" "$tab")"
    printf '{"result":{"agent":{"name":"%s"}}}\n' "$name"; exit 0 ;;
  "pane split")
    parent="$3"; shift 3; cwd=""
    while [ $# -gt 0 ]; do case "$1" in --cwd) cwd="$2"; shift 2 ;; *) shift ;; esac; done
    IFS=$'\t' read -r _ _ _ _ tab <<<"$(row 1 "$parent")"
    n=$(( $(cat "$FAKE/next" 2>/dev/null || echo 10) + 1 )); printf '%s\n' "$n" > "$FAKE/next"
    pane="${parent%%:*}:p$n"
    # A shell until an agent starts in it.
    printf '%s\t-\t%s\tshell\t%s\n' "$pane" "$cwd" "$tab" >> "$A"
    printf '{"result":{"pane":{"pane_id":"%s"}}}\n' "$pane"; exit 0 ;;
  "pane close")
    set_row "$3" ""; printf '{"result":{}}\n'; exit 0 ;;
  "pane run"|"pane rename"|"pane title"|"pane report-metadata"|"tab rename"|"tab set-title")
    printf '{"result":{}}\n'; exit 0 ;;
esac
exec "$(dirname "$0")/herdr.fake" "$@"
STUB
chmod +x "$TMP/bin/herdr"

MAIN="$TMP/main"; WT_B="$TMP/wt-b"; WT_C="$TMP/wt-c"
mkdir -p "$MAIN"; git -C "$MAIN" init -q
git -C "$MAIN" config user.email t@t; git -C "$MAIN" config user.name t
printf '.tandem-relay/\n' > "$MAIN/.gitignore"
printf '{"builder_kind":"codex","reviewer":{"kind":"codex"}}\n' > "$MAIN/.tandem.json"
printf '# Plan\n\n**Goal:** g.\n\n### Task 1: First\n\nfirst thing\n' > "$MAIN/plan.md"
git -C "$MAIN" add . && git -C "$MAIN" commit -qm init
git -C "$MAIN" worktree add -q "$WT_B" 2>/dev/null
MAIN=$(cd "$MAIN" && pwd -P); WT_B=$(cd "$WT_B" && pwd -P); WT_C="${WT_B%/*}/wt-c"

# Two Claude planners, each in its own tab and checkout, both still unnamed.
printf 'w1:p1\t-\t%s\tclaude\tw1:t1\nw2:p1\t-\t%s\tclaude\tw2:t1\n' "$MAIN" "$WT_B" > "$FAKE/agents"
FAKE="$FAKE" PATH="$TMP/bin:$PATH" fake_socket_start "$TMP" || exit 1

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
pair() { as_pane "$1" "$2" "$ROOT_DIR/bin/tandem-pair" --skip-doctor --approve-kind codex --cwd "$1"; }
# pane_of NAME: the pane holding agent NAME.
pane_of() { awk -F'\t' -v n="$1" '$2 == n { print $1; exit }' "$FAKE/agents"; }
names_in() { awk -F'\t' -v t="$1" '$5 == t && $2 != "-" { print $2 }' "$FAKE/agents" | sort | tr '\n' ' '; }
# last_to: the agent the latest prompt went to.
last_to() { tail -1 "$FAKE/prompts" 2>/dev/null | cut -f1; }
prompts() { [ -f "$FAKE/prompts" ] && wc -l < "$FAKE/prompts" | tr -d ' ' || echo 0; }
bridge_of() { cat "$1/.tandem-relay/bridge.pid" 2>/dev/null; }
record_of() { cat "$1/.tandem-relay/pair.json" 2>/dev/null; }

printf '\n== each checkout pairs under its own names ==\n'
OUT=$(pair "$MAIN" w1:p1); rc=$?
[ "$rc" -eq 0 ] && ok "the main checkout pairs" || nope "main pair rc $rc: $OUT"
fake_set_state idle
OUT=$(pair "$WT_B" w2:p1); rc=$?
[ "$rc" -eq 0 ] && ok "the worktree pairs beside it" || nope "wt-b pair rc $rc: $OUT"
fake_set_state idle
[ "$(names_in w1:t1)" = "builder planner reviewer " ] && ok "the main checkout's agents are planner, builder and reviewer" \
  || nope "main agents: $(names_in w1:t1)"
[ "$(names_in w2:t1)" = "builder-wt-b planner-wt-b reviewer-wt-b " ] && ok "wt-b's are planner-wt-b, builder-wt-b and reviewer-wt-b" \
  || nope "wt-b agents: $(names_in w2:t1)"
[ "$(record_of "$MAIN")" = '{"planner":"planner","builder":"builder","reviewer":"reviewer"}' ] \
  && ok "the main checkout records its own names" || nope "main pair.json: $(record_of "$MAIN")"
[ "$(record_of "$WT_B")" = '{"planner":"planner-wt-b","builder":"builder-wt-b","reviewer":"reviewer-wt-b"}' ] \
  && ok "wt-b records its own names" || nope "wt-b pair.json: $(record_of "$WT_B")"
MAIN_BRIDGE=$(bridge_of "$MAIN"); B_BRIDGE=$(bridge_of "$WT_B")
if [ -n "$MAIN_BRIDGE" ] && [ -n "$B_BRIDGE" ] && [ "$MAIN_BRIDGE" != "$B_BRIDGE" ] \
  && kill -0 "$MAIN_BRIDGE" 2>/dev/null && kill -0 "$B_BRIDGE" 2>/dev/null; then
  ok "each checkout runs its own bridge"
else
  nope "bridges: main '$MAIN_BRIDGE', wt-b '$B_BRIDGE'"
fi
case "$(ps -p "$B_BRIDGE" -o command= 2>/dev/null)" in
  *"--planner planner-wt-b"*) ok "and wt-b's bridge delivers to planner-wt-b" ;;
  *) nope "wt-b bridge: $(ps -p "$B_BRIDGE" -o command= 2>/dev/null)" ;;
esac

printf '\n== a verdict reaches only its own planner ==\n'
printf 'looks clean\n' > "$TMP/review.md"
# verdict DIR REVIEWER: run tandem-verdict in DIR as the agent REVIEWER.
verdict() {
  as_pane "$1" "$(pane_of "$2")" env HERDR_ROLE="$2" "$ROOT_DIR/bin/tandem-verdict" \
    --plan "$1/plan.md" --review "$TMP/review.md" --verdict clean
}
for c in "$MAIN reviewer planner" "$WT_B reviewer-wt-b planner-wt-b"; do
  read -r dir who to <<<"$c"; before=$(prompts)
  OUT=$(verdict "$dir" "$who"); rc=$?; fake_set_state idle
  if [ "$rc" -eq 0 ] && [ "$(prompts)" -eq $((before + 1)) ] && [ "$(last_to)" = "$to" ]; then
    ok "$who's verdict reaches $to and no one else"
  else
    nope "$who's verdict: rc $rc, $(( $(prompts) - before )) prompts, last to '$(last_to)': $OUT"
  fi
done

printf '\n== a handback reaches only its own planner ==\n'
printf 'all done\n' > "$TMP/report.md"
for c in "$MAIN builder planner" "$WT_B builder-wt-b planner-wt-b"; do
  read -r dir who to <<<"$c"; before=$(prompts)
  OUT=$(as_pane "$dir" "$(pane_of "$who")" env HERDR_ROLE="$who" "$ROOT_DIR/bin/tandem-handback" --report "$TMP/report.md" --plan "$dir/plan.md")
  rc=$?; fake_set_state idle
  if [ "$rc" -eq 0 ] && [ "$(prompts)" -eq $((before + 1)) ] && [ "$(last_to)" = "$to" ]; then
    ok "$who's handback reaches $to and no one else"
  else
    nope "$who's handback: rc $rc, $(( $(prompts) - before )) prompts, last to '$(last_to)': $OUT"
  fi
done

printf '\n== a moved worktree keeps its recorded names ==\n'
git -C "$MAIN" worktree move "$WT_B" "$WT_C" || nope "git worktree move failed"
# The agents' shells moved with the folder, so Herdr reports the new cwd.
sed "s#	$WT_B	#	$WT_C	#" "$FAKE/agents" > "$FAKE/agents.new" && mv "$FAKE/agents.new" "$FAKE/agents"
before=$(prompts)
OUT=$(verdict "$WT_C" reviewer-wt-b); rc=$?; fake_set_state idle
if [ "$rc" -eq 0 ] && [ "$(prompts)" -eq $((before + 1)) ] && [ "$(last_to)" = planner-wt-b ]; then
  ok "a verdict from wt-c still reaches planner-wt-b"
else
  nope "moved verdict: rc $rc, last to '$(last_to)': $OUT"
fi

printf '\n== tandem-unpair in the worktree closes only its own pair ==\n'
OUT=$(as_pane "$WT_C" w2:p1 "$ROOT_DIR/bin/tandem-unpair" --force --skip-checks); rc=$?
[ "$rc" -eq 0 ] && ok "tandem-unpair runs in the moved worktree" || nope "unpair rc $rc: $OUT"
[ "$(names_in w2:t1)" = "planner-wt-b " ] && ok "wt-b's builder and reviewer panes are closed" || nope "wt-b agents left: $(names_in w2:t1)"
[ "$(names_in w1:t1)" = "builder planner reviewer " ] && ok "the main checkout's panes are untouched" || nope "main agents: $(names_in w1:t1)"
# exits_within PID: the process ends within 5 s. A killed bridge exits once its
# event wait returns, which the 1 s backstop below bounds.
exits_within() { local i; for i in $(seq 1 50); do kill -0 "$1" 2>/dev/null || return 0; sleep 0.1; done; return 1; }
[ -n "$B_BRIDGE" ] && exits_within "$B_BRIDGE" && ok "wt-b's bridge is stopped" || nope "wt-b's bridge '$B_BRIDGE' still runs: $OUT"
kill -0 "$MAIN_BRIDGE" 2>/dev/null && ok "the main checkout's bridge keeps running" || nope "the main bridge stopped"

printf '\ntests/test-pair-worktrees: passed %d, failed %d\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

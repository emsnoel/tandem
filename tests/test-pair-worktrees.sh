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
#   - tandem-unpair in that worktree closes only its own panes;
#   - a builder or reviewer from another checkout is refused with exit 35, even
#     under a forced name or from a separate repository, while one in a
#     subdirectory of the checkout or behind a symlink to it is adopted.
set -uo pipefail
ROOT_DIR=$(cd "$(dirname "$0")/.." && pwd)

PASS=0; FAIL=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }

TMP=$(mktemp -d)
cleanup() {
  local f p
  for f in "$TMP"/main/.tandem-relay/bridge.pid "$TMP"/wt-*/.tandem-relay/bridge.pid "$TMP"/other/.tandem-relay/bridge.pid; do
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

. "$ROOT_DIR/tests/lib/fake-herdr-panes.sh"
fake_herdr_panes_install "$TMP/bin"

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
# event wait returns, which the 1 s backstop below bounds.
exits_within() { local _; for _ in $(seq 1 50); do kill -0 "$1" 2>/dev/null || return 0; sleep 0.1; done; return 1; }

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

printf '\n== a pair whose planner is gone can be cleared and paired again ==\n'
OLD_BRIDGE=$(bridge_of "$WT_B")
# The planner's pane closes. Its bridge, builder and reviewer keep running, and
# the person opens a new pane in the same tab and checkout.
as_pane "$WT_B" w2:p1 herdr pane close w2:p1 >/dev/null
printf 'w2:p9\t-\t%s\tclaude\tw2:t1\n' "$WT_B" >> "$FAKE/agents"
# A planner under another name is refused, and the refusal names the way out.
OUT=$(HERDR_PLANNER_NAME=planner-alt pair "$WT_B" w2:p9); rc=$?
CMD=$(printf '%s\n' "$OUT" | sed -n 's/.*Clear the old pair with: //p')
[ "$rc" -ne 0 ] && [ "$CMD" = "HERDR_PLANNER_NAME=planner-wt-b tandem-unpair --force" ] \
  && ok "a planner under another name is told to clear the old pair" || nope "refusal: rc $rc, '$CMD': $OUT"
# Run the printed command exactly as printed, from the new pane.
OUT=$(as_pane "$WT_B" w2:p9 bash -c "$CMD"); rc=$?
[ "$rc" -eq 0 ] && ok "the printed unpair command succeeds from another pane" || nope "unpair rc $rc: $OUT"
[ -z "$(names_in w2:t1)" ] && ok "wt-b's builder and reviewer panes are closed" || nope "wt-b agents left: $(names_in w2:t1)"
[ "$(names_in w1:t1)" = "builder planner reviewer " ] && ok "the main checkout's panes are untouched" || nope "main agents: $(names_in w1:t1)"
[ -n "$OLD_BRIDGE" ] && exits_within "$OLD_BRIDGE" && ok "wt-b's bridge is stopped" || nope "wt-b's bridge '$OLD_BRIDGE' still runs"
[ ! -e "$WT_B/.tandem-relay" ] && ok "wt-b's relay is removed" || nope "wt-b relay left: $(ls -A "$WT_B/.tandem-relay")"
kill -0 "$MAIN_BRIDGE" 2>/dev/null && [ -f "$MAIN/.tandem-relay/pair.json" ] \
  && ok "the main checkout's bridge and relay are untouched" || nope "the main pair was disturbed"
OUT=$(pair "$WT_B" w2:p9); rc=$?; fake_set_state idle
[ "$rc" -eq 0 ] && ok "wt-b pairs again from the new pane" || nope "re-pair rc $rc: $OUT"
[ "$(names_in w2:t1)" = "builder-wt-b planner-wt-b reviewer-wt-b " ] && ok "under the same names" || nope "wt-b agents: $(names_in w2:t1)"
B_BRIDGE=$(bridge_of "$WT_B")

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
OUT=$(as_pane "$WT_C" w2:p9 "$ROOT_DIR/bin/tandem-unpair" --force --skip-checks); rc=$?
[ "$rc" -eq 0 ] && ok "tandem-unpair runs in the moved worktree" || nope "unpair rc $rc: $OUT"
[ "$(names_in w2:t1)" = "planner-wt-b " ] && ok "wt-b's builder and reviewer panes are closed" || nope "wt-b agents left: $(names_in w2:t1)"
[ "$(names_in w1:t1)" = "builder planner reviewer " ] && ok "the main checkout's panes are untouched" || nope "main agents: $(names_in w1:t1)"
[ -n "$B_BRIDGE" ] && exits_within "$B_BRIDGE" && ok "wt-b's bridge is stopped" || nope "wt-b's bridge '$B_BRIDGE' still runs: $OUT"
kill -0 "$MAIN_BRIDGE" 2>/dev/null && ok "the main checkout's bridge keeps running" || nope "the main bridge stopped"

printf '\n== a builder or reviewer from another checkout is refused ==\n'
MAIN_RECORD=$(record_of "$MAIN"); MAIN_ARGS=$(ps -p "$MAIN_BRIDGE" -o command= 2>/dev/null)
MAIN_BUILDER=$(pane_of builder); MAIN_REVIEWER=$(pane_of reviewer)
AGENTS=$(cat "$FAKE/agents")
# refused OUT NAME PANE DIR: OUT is the exit-35 refusal for agent NAME in PANE,
# which works in DIR.
refused() {
  printf '%s\n' "$1" | grep -qF "tandem-pair: the agent named '$2' in pane $3 works in another checkout: $4." \
    && printf '%s\n' "$1" | grep -qF "tandem does not adopt it, because its messages would go to that checkout's pair." \
    && printf '%s\n' "$1" | grep -qF "close it from that checkout with tandem-unpair, or pair here under other names (HERDR_BUILDER_NAME, HERDR_REVIEWER_NAME)."
}
# untouched: the main checkout's record, bridge and every pane are as they were.
untouched() {
  [ "$(record_of "$MAIN")" = "$MAIN_RECORD" ] && kill -0 "$MAIN_BRIDGE" 2>/dev/null \
    && [ "$(ps -p "$MAIN_BRIDGE" -o command= 2>/dev/null)" = "$MAIN_ARGS" ] && [ "$(cat "$FAKE/agents")" = "$AGENTS" ]
}
# The worktree's planner forces the main checkout's builder name.
OUT=$(HERDR_PLANNER_NAME=planner-b HERDR_BUILDER_NAME=builder HERDR_REVIEWER_NAME=reviewer pair "$WT_C" w2:p9 --dry-run); rc=$?
[ "$rc" -eq 35 ] && refused "$OUT" builder "$MAIN_BUILDER" "$MAIN" \
  && ok "a dry run refuses the main checkout's builder with exit 35" || nope "dry run: rc $rc: $OUT"
untouched && ok "and the main checkout's record, bridge and panes are unchanged" || nope "main pair changed: $(record_of "$MAIN")"
OUT=$(HERDR_PLANNER_NAME=planner-b HERDR_BUILDER_NAME=builder HERDR_REVIEWER_NAME=reviewer pair "$WT_C" w2:p9); rc=$?
[ "$rc" -eq 35 ] && refused "$OUT" builder "$MAIN_BUILDER" "$MAIN" \
  && ok "a real run refuses it with exit 35" || nope "real run: rc $rc: $OUT"
[ ! -e "$WT_C/.tandem-relay" ] && ok "the worktree gets no relay" || nope "wt-c relay: $(ls -A "$WT_C/.tandem-relay")"
untouched && ok "and nothing was renamed or started" || nope "agents changed: $(cat "$FAKE/agents")"
# Only the reviewer's name clashes. It is refused before the planner is renamed.
OUT=$(HERDR_PLANNER_NAME=planner-b HERDR_REVIEWER_NAME=reviewer pair "$WT_C" w2:p9); rc=$?
[ "$rc" -eq 35 ] && refused "$OUT" reviewer "$MAIN_REVIEWER" "$MAIN" \
  && ok "the main checkout's reviewer is refused with exit 35" || nope "reviewer: rc $rc: $OUT"
untouched && [ ! -e "$WT_C/.tandem-relay" ] && ok "and nothing was renamed, started or recorded" \
  || nope "after the reviewer refusal: $(cat "$FAKE/agents")"

printf '\n== a builder in a subdirectory or behind a symlink is adopted ==\n'
# set_cwd PANE DIR: Herdr reports DIR as PANE's cwd.
set_cwd() {
  awk -F'\t' -v OFS='\t' -v p="$1" -v d="$2" '$1 == p { $3 = d } { print }' "$FAKE/agents" > "$FAKE/agents.new" \
    && mv "$FAKE/agents.new" "$FAKE/agents"
}
mkdir -p "$MAIN/sub/dir"; ln -s "$MAIN" "$TMP/main-link"
for d in "$MAIN/sub/dir" "$TMP/main-link"; do
  label="main/sub/dir"; [ "$d" = "$TMP/main-link" ] && label="a symlink to main"
  set_cwd "$MAIN_BUILDER" "$d"
  OUT=$(pair "$MAIN" w1:p1); rc=$?; fake_set_state idle
  if [ "$rc" -eq 0 ] && [ "$(pane_of builder)" = "$MAIN_BUILDER" ] && [ "$(record_of "$MAIN")" = "$MAIN_RECORD" ]; then
    ok "a builder working in $label is adopted"
  else
    nope "builder in $d: rc $rc: $OUT"
  fi
done
set_cwd "$MAIN_BUILDER" "$MAIN"
MAIN_BRIDGE=$(bridge_of "$MAIN")
# builder.json follows the builder's cwd, where its readers look; the bridge
# stays at the checkout's top.
[ ! -e "$MAIN/sub/dir/.tandem-relay/bridge.pid" ] && kill -0 "$MAIN_BRIDGE" 2>/dev/null \
  && ok "the relay bridge stays at the checkout's top" || nope "bridge moved: $(ls -A "$MAIN/sub/dir/.tandem-relay")"

printf '\n== a separate repository does not adopt the first one'"'"'s builder ==\n'
OTHER="$TMP/other"; mkdir -p "$OTHER"; git -C "$OTHER" init -q
git -C "$OTHER" config user.email t@t; git -C "$OTHER" config user.name t
cp "$MAIN/.gitignore" "$MAIN/.tandem.json" "$OTHER/"
git -C "$OTHER" add . && git -C "$OTHER" commit -qm init
OTHER=$(cd "$OTHER" && pwd -P)
printf 'w3:p1\t-\t%s\tclaude\tw3:t1\n' "$OTHER" >> "$FAKE/agents"
AGENTS=$(cat "$FAKE/agents")
# Both are main checkouts, so both derive planner, builder and reviewer.
OUT=$(pair "$OTHER" w3:p1); rc=$?
[ "$rc" -ne 0 ] && printf '%s\n' "$OUT" | grep -qF "the name 'planner' is already held by pane w1:p1" \
  && ok "the second repository's planner name clash is refused" || nope "planner clash: rc $rc: $OUT"
OUT=$(HERDR_PLANNER_NAME=planner-o pair "$OTHER" w3:p1); rc=$?
[ "$rc" -eq 35 ] && refused "$OUT" builder "$MAIN_BUILDER" "$MAIN" \
  && ok "under another planner name it refuses the first repository's builder with exit 35" || nope "other repo: rc $rc: $OUT"
[ "$(cat "$FAKE/agents")" = "$AGENTS" ] && [ ! -e "$OTHER/.tandem-relay/pair.json" ] \
  && ok "and nothing was renamed or recorded" || nope "after the other repo's refusal: $(cat "$FAKE/agents")"
# An unnamed agent beside the planner that works in the first repository is not
# adopted either.
printf 'w3:p2\t-\t%s\tcodex\tw3:t1\n' "$MAIN" >> "$FAKE/agents"
AGENTS=$(cat "$FAKE/agents")
OUT=$(HERDR_PLANNER_NAME=planner-o HERDR_BUILDER_NAME=builder-o HERDR_REVIEWER_NAME=reviewer-o pair "$OTHER" w3:p1); rc=$?
[ "$rc" -eq 35 ] && printf '%s\n' "$OUT" | grep -qF "tandem-pair: the unnamed agent in pane w3:p2 works in another checkout: $MAIN." \
  && ok "an unnamed agent from the first repository is not adopted" || nope "adoption: rc $rc: $OUT"
[ "$(cat "$FAKE/agents")" = "$AGENTS" ] && ok "and nothing was renamed" || nope "after the adoption refusal: $(cat "$FAKE/agents")"

printf '\ntests/test-pair-worktrees: passed %d, failed %d\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

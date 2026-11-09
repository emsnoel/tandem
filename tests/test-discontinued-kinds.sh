#!/usr/bin/env bash
# A pair made before an upgrade can still name a kind tandem no longer supports.
# tandem-pair refuses to use a pane running one, and tandem-handoff refuses to
# type into a pair recorded with one, on both engines.
#
# Herdr is stubbed and tandem is the real binary. --dry-run keeps tandem-pair
# from touching a pane.
set -uo pipefail
ROOT_DIR=$(cd "$(dirname "$0")/.." && pwd)

PASS=0; FAIL=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }
has()  { case "$OUT" in *"$2"*) ok "$1" ;; *) nope "$1 (missing: $2): $OUT" ;; esac; }
lack() { case "$OUT" in *"$2"*) nope "$1 (found: $2): $OUT" ;; *) ok "$1" ;; esac; }
exits() { [ "$RC" -eq "$2" ] && ok "$1" || nope "$1 (exit $RC, want $2): $OUT"; }

TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
REPO=$(mkdir -p "$TMP/repo" && cd "$TMP/repo" && pwd -P)
mkdir -p "$TMP/bin" "$TMP/home"
git -C "$REPO" init -q
printf '.tandem-relay/\n' > "$REPO/.gitignore"
printf '# Plan\n\n**Goal:** g.\n\n### Task 1: One\n\nDo it.\n' > "$REPO/plan.md"
git -C "$REPO" add . && git -C "$REPO" -c user.name=t -c user.email=t@t commit -qm plan

go -C "$ROOT_DIR" build -o "$TMP/tandem-real" ./cmd/tandem \
  || { echo "could not build tandem" >&2; exit 2; }
# The plan has cleared review; everything else is the shipped binary.
printf '#!/usr/bin/env bash\n[ "$1 $2" = "gate review" ] && exit 0\nexec "%s" "$@"\n' "$TMP/tandem-real" > "$TMP/bin/tandem"
chmod +x "$TMP/bin/tandem"
printf '#!/usr/bin/env bash\nexit 0\n' > "$TMP/bin/tandem-review"

# AGENTS is "pane:kind:name ..." beside the planner, whose pane is p1. An empty
# name is an unnamed agent. Every call that would type is logged to CALLS.
cat > "$TMP/bin/herdr" <<'STUB'
#!/usr/bin/env bash
agent_json() {
  printf '{"pane_id":"%s","agent":"%s","name":"%s","cwd":"%s","agent_status":"idle","tab_id":"t1"}' "$1" "$2" "$3" "$REPO"
}
case "$1 $2" in
  "agent list")
    printf '{"result":{"agents":['; agent_json p1 claude planner
    for a in ${AGENTS:-}; do
      IFS=: read -r p k n <<<"$a"
      printf ','; agent_json "$p" "$k" "$n"
    done
    printf ']}}' ;;
  "agent get")
    for a in ${AGENTS:-}; do
      IFS=: read -r p k n <<<"$a"
      if [ "$n" = "$3" ]; then printf '{"result":{"agent":'; agent_json "$p" "$k" "$n"; printf '}}'; exit 0; fi
    done
    printf '{"error":{"code":"agent_not_found","message":"agent target %s not found"}}' "$3" >&2
    exit 1 ;;
  "pane layout") printf '{"result":{"layout":{"panes":[{"pane_id":"p1","rect":{"width":200,"height":50}}]}}}' ;;
  "agent prompt"|"agent reset") printf '%s\n' "$*" >> "$CALLS"; printf '{"result":{}}' ;;
  *) printf '{"error":{"code":"unexpected","message":"stub got: %s"}}' "$*" >&2; exit 1 ;;
esac
STUB
chmod +x "$TMP/bin/herdr"
CALLS="$TMP/calls"; export REPO CALLS

run_pair() {
  local agents="$1"; shift
  OUT=$(cd "$REPO" && env PATH="$TMP/bin:$PATH" HOME="$TMP/home" HERDR_ENV=1 HERDR_PANE_ID=p1 \
    HERDR_TANDEM_CONFIG="$TMP/home/tandem.json" HERDR_SKIP_HOOK_CHECK=1 AGENTS="$agents" \
    "$ROOT_DIR/bin/tandem-pair" --dry-run --skip-doctor --cwd "$REPO" "$@" 2>&1); RC=$?
}

refused_pane() {
  exits "$1: exits 20" 20
  has   "$1: names the pane and the kind" "pane $2 runs opencode"
  has   "$1: says to close it" "close that pane"
  lack  "$1: does not adopt it" "adopting"
  lack  "$1: does not report a pair" "pair ready"
  lack  "$1: does not say it is present" "already present"
  lack  "$1: records nothing" "would record builder"
}

printf '\n== tandem-pair refuses a pane running OpenCode ==\n'
run_pair "p2:opencode:" --builder-autonomy auto --reviewer claude
refused_pane "an unnamed pane" p2

run_pair "p2:opencode:" --builder codex --reviewer claude
refused_pane "an unnamed pane when codex is asked for" p2

run_pair "p2:opencode:builder" --builder codex --reviewer claude
refused_pane "a pane named builder, repaired with --builder codex" p2

run_pair "p3:opencode:reviewer" --builder codex --reviewer claude
exits "a pane named reviewer: exits 20" 20
has   "a pane named reviewer: names the pane, the kind and the role" "pane p3 runs opencode, which tandem no longer supports, so it cannot be the reviewer"
lack  "a pane named reviewer: reports no pair" "pair ready"

run_pair "p2:codex:" --builder codex --reviewer claude
exits "a supported unnamed pane is still adopted: exits 0" 0
has   "a supported unnamed pane is still adopted" "adopting the running codex agent"

printf '\n== tandem-pair will not approve a kind with no adapter ==\n'
run_pair "" --approve-kind opencode --builder codex --reviewer claude
exits "--approve-kind opencode: exits 20" 20
has   "--approve-kind opencode: names the kind and the supported kinds" 'kind "opencode" is not supported, so it cannot be approved. Use one of the supported kinds: claude, codex, copilot'
lack  "--approve-kind opencode: no second, vaguer line" "could not record"
[ ! -e "$TMP/home/tandem.json" ] && ok "--approve-kind opencode: writes no user config" || nope "--approve-kind opencode wrote the user config"

printf '\n== tandem-handoff refuses a recorded OpenCode pair ==\n'
RELAY="$REPO/.tandem-relay"
reset_relay() {
  rm -rf "$RELAY" "$CALLS"; mkdir -p "$RELAY"
  printf '{"kind":"%s","autonomy":"auto"}\n' "$1" > "$RELAY/builder.json"
  printf '{"kind":"%s","name":"reviewer","autonomy":"auto"}\n' "$2" > "$RELAY/reviewer.json"
}
run_handoff() {
  local engine="$1"; shift
  OUT=$(cd "$REPO" && env PATH="$TMP/bin:$PATH" HOME="$TMP/home" HERDR_ENV=1 HERDR_PANE_ID=p1 \
    HERDR_TANDEM_CONFIG="$TMP/home/tandem.json" TANDEM_BUILD_ENGINE="$engine" TANDEM_STATE_HOME="$TMP/state" \
    AGENTS="p2:codex:builder p3:claude:reviewer" "$ROOT_DIR/bin/tandem-handoff" "$REPO/plan.md" "$@" 2>&1); RC=$?
}
refused_handoff() {
  exits "$1: exits 20" 20
  has   "$1: names the kind and the role" "records opencode as its $2"
  has   "$1: says it is discontinued" "discontinued opencode"
  has   "$1: says to re-pair" "re-pair with a supported kind: tandem-pair --$2 KIND (one of claude, codex, copilot)"
  has   "$1: says nothing was sent" "Nothing was sent"
  lack  "$1: not the start's flag refusal" "--builder-kind"
  [ ! -e "$RELAY/build/build.json" ] && ok "$1: writes no build" || nope "$1: wrote build/build.json"
  [ ! -e "$RELAY/active-run.json" ] && ok "$1: starts no run" || nope "$1: started a run"
  [ ! -s "$CALLS" ] && ok "$1: types nothing" || nope "$1: typed: $(cut -c1-60 "$CALLS" | head -3)"
}

for engine in "" durable; do
  label="${engine:-the default engine}"
  reset_relay opencode claude; run_handoff "$engine"
  refused_handoff "builder on $label" builder
  reset_relay codex opencode; run_handoff "$engine"
  refused_handoff "reviewer on $label" reviewer
  reset_relay opencode claude; run_handoff "$engine" --dry-run
  refused_handoff "--dry-run on $label" builder
done

# The relay bridge continues a legacy build with --continue, and that would type too.
reset_relay opencode claude; run_handoff "" --continue
refused_handoff "--continue with no run" builder

reset_relay codex claude; run_handoff "" --dry-run
lack "a supported pair passes this check" "discontinued"

printf '\ndiscontinued-kinds: passed %d, failed %d\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

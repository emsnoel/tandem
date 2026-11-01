#!/usr/bin/env bash
# Teardown and version routing for a durable run, with the real binary.
#
#   - tandem-unpair stops, settles and archives a durable run and removes only
#     the relay; the run's rows and artifacts stay readable.
#   - Every shell entry point refuses a relay from an unknown engine or
#     protocol before writing or deleting anything.
#
# Herdr calls are stubbed (the builder pane is gone); `tandem build` and
# `tandem state` are the binary built from this tree.
set -uo pipefail
. "$(cd "$(dirname "$0")" && pwd)/lib/records.sh"
ROOT_DIR=$(cd "$(dirname "$0")/.." && pwd)

PASS=0; FAIL=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }

command -v jq >/dev/null || { echo "need jq" >&2; exit 2; }
TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
mkdir -p "$TMP/bin"
(cd "$ROOT_DIR" && go build -o "$TMP/tandem-real" ./cmd/tandem) || { echo "could not build tandem"; exit 1; }
# A stub jq that fails: tandem-unpair asks tandem, so any jq call is a bug.
printf '#!/bin/sh\necho "jq was called: $*" >&2\nexit 99\n' > "$TMP/bin/jq"
. "$ROOT_DIR/tests/lib/fake-herdr.sh"
. "$ROOT_DIR/tests/lib/fake-socket.sh"
fake_herdr_install "$TMP/bin" "$TMP/fake"
FAKE="$TMP/fake" PATH="$TMP/bin:$PATH" fake_socket_start "$TMP" || exit 1
# The pane is gone (exit 10) unless PANE names a live, idle one.
cat > "$TMP/bin/tandem" <<'STUB'
#!/usr/bin/env bash
case "$1 $2" in
  "agent snapshot") [ -n "${PANE:-}" ] || exit 10; printf '%s\t%s\tidle\n' "$PANE" "$REPO"; exit 0 ;;
  "agent status")   [ -n "${PANE:-}" ] || exit 1; printf 'idle\n'; exit 0 ;;
  "config check-command") printf 'true\0'; exit 0 ;;
esac
case "$1" in
  pane) exit 0 ;;
esac
exec "$TANDEM_REAL" "$@"
STUB
chmod +x "$TMP/bin/tandem" "$TMP/bin/jq"

export TANDEM_REAL="$TMP/tandem-real" TANDEM_STATE_HOME="$TMP/state" FAKE="$TMP/fake" HERDR_ENV=1
PATHX="$TMP/bin:$ROOT_DIR/bin:/usr/bin:/bin"

mkrepo() { # mkrepo DIR -> a committed repo with a two-task plan and a codex builder
  rm -rf "$1"; mkdir -p "$1/.tandem-relay"; git -C "$1" init -q
  git -C "$1" config user.email t@t; git -C "$1" config user.name t
  printf '.tandem-relay/\n.tandem-history/\n' > "$1/.gitignore"
  write_builder "$1/.tandem-relay"
  printf '# Plan\n\n**Goal:** g.\n\n### Task 1: First\n\nfirst thing\n\n### Task 2: Second\n\nsecond thing\n' > "$1/plan.md"
  git -C "$1" add . && git -C "$1" commit -qm plan
}
# tree DIR: every file with its content hash.
tree() { (cd "$1" && find . -type f -print0 | sort -z | xargs -0 shasum -a 256 2>/dev/null); }
td() { env PATH="$PATHX" REPO="$R" "$@"; }

printf '\n== unpairing a durable run keeps its state ==\n'
R="$TMP/repo"; mkrepo "$R"; R=$(cd "$R" && pwd -P)
OUT=$(td env TANDEM_BUILD_ENGINE=durable tandem build start --plan "$R/plan.md" --root "$R" --builder builder --planner planner --builder-kind codex --reviewer reviewer --reviewer-kind claude --check true 2>&1)
RUN=$(printf '%s' "$OUT" | jq -r '.run_id' 2>/dev/null)
[[ "$RUN" =~ ^[0-9a-f]{32}$ ]] && ok "a durable run starts (run $RUN)" || { nope "start: $OUT"; exit 1; }
[ -f "$R/.tandem-relay/active-run.json" ] && ok "the relay holds its marker" || nope "no marker"
fake_set_state idle
OUT=$(cd "$R" && env PATH="$PATHX" REPO="$R" TANDEM_UNPAIR_SETTLE_POLLS=3 tandem-unpair --skip-checks 2>&1); rc=$?
[ "$rc" -eq 0 ] && ok "unpair succeeds" || { nope "unpair rc $rc: $OUT"; }
case "$OUT" in *"archived durable run $RUN"*) ok "it says the run was archived" ;; *) nope "no archive line: $OUT" ;; esac
[ ! -e "$R/.tandem-relay" ] && ok "the relay is gone" || nope "the relay is still there"
ST=$(td tandem state status --json --root "$R" | jq -r '"\(.initialized) \(.readable) \(.current_run.state // "none")"')
case "$ST" in "true true "*) ok "the state is still initialized and readable ($ST)" ;; *) nope "status: $ST" ;; esac
EX="$TMP/export"
EXOUT=$(td tandem state export --run "$RUN" --output "$EX" --root "$R" 2>"$TMP/err")
[ -f "$EX/COMPLETE" ] && ok "the run can still be exported, bytes and all" || nope "export: $(cat "$TMP/err")"
[ "$(printf '%s' "$EXOUT" | jq -r .state)" = archived ] && ok "and it is recorded as archived history" || nope "export state: $EXOUT"
td tandem state gc --root "$R" >/dev/null 2>&1 && ok "and a retention report still runs" || nope "gc report failed"
[ "$(td tandem state gc --root "$R" | jq '.candidates | length')" -ge 0 ] && ok "(its report is JSON)" || nope "gc output"

printf '\n== a durable run that has not settled blocks the teardown ==\n'
R2="$TMP/repo2"; mkrepo "$R2"; R2=$(cd "$R2" && pwd -P); R="$R2"
td env TANDEM_BUILD_ENGINE=durable tandem build start --plan "$R2/plan.md" --root "$R2" --builder builder --planner planner --builder-kind codex --reviewer reviewer --reviewer-kind claude --check true >/dev/null 2>&1
fake_set_state idle; : > "$FAKE/prompts"
td tandem build tick --root "$R2" >/dev/null 2>&1   # the task prompt is typed; the builder is now working
[ "$(fake_status)" = working ] || nope "setup: the builder is not working"
BEFORE=$(tree "$R2/.tandem-relay")
OUT=$(cd "$R2" && env PATH="$PATHX" REPO="$R2" TANDEM_UNPAIR_SETTLE_POLLS=1 tandem-unpair --skip-checks 2>&1); rc=$?
[ "$rc" -ne 0 ] && ok "unpair refuses while the builder is still working" || nope "unpair rc $rc"
case "$OUT" in *"not settled"*) ok "and says why" ;; *) nope "no reason: $OUT" ;; esac
[ -d "$R2/.tandem-relay" ] && ok "nothing was deleted" || nope "the relay was removed"
fake_set_state idle

printf '\n== a live pane: a durable run is signed off on its own evidence ==\n'
# finish_tasks N: play the builder and the reviewer for the first N tasks of
# the run in $R, through the shipped CLI and the fake Herdr.
finish_tasks() {
  local n line p att job eff nonce rj ra rp rt
  for n in $(seq 1 "$1"); do
    fake_set_state idle; td tandem build tick --root "$R" >/dev/null 2>&1
    line=$(grep -n 'Implement task' "$FAKE/prompts" | tail -1 | cut -d: -f1); p=$(cat "$FAKE/prompt.$line.txt")
    att=$(printf '%s' "$p" | grep -o -- '--attempt [0-9a-f]*' | head -1 | cut -d' ' -f2)
    job=$(printf '%s' "$p" | grep -o -- '--job [0-9a-f]*' | head -1 | cut -d' ' -f2)
    eff=$(printf '%s' "$p" | grep -o -- '--effect [0-9a-f]*' | head -1 | cut -d' ' -f2)
    nonce=$(printf '%s' "$p" | grep -o -- '--nonce [0-9a-f]*' | head -1 | cut -d' ' -f2)
    td tandem build event new --root "$R" --kind builder_started --task "$n" --attempt "$att" --job "$job" --effect "$eff" --nonce "$nonce" >/dev/null 2>&1
    printf 'task %s\n' "$n" > "$R/work$n.txt"; printf 'done\n' > "$TMP/report.md"
    td tandem build event new --root "$R" --kind builder_submitted --task "$n" --attempt "$att" --job "$job" --effect "$eff" --nonce "$nonce" --payload "$TMP/report.md" >/dev/null 2>&1
    fake_set_state idle; td tandem build tick --root "$R" >/dev/null 2>&1
    line=$(grep -n '"job_id"' "$FAKE/prompts" | tail -1 | cut -d: -f1); p=$(cat "$FAKE/prompt.$line.txt")
    rj=$(printf '%s' "$p" | grep -o '"job_id":"[0-9a-f]*"' | head -1 | cut -d'"' -f4)
    rp=$(printf '%s' "$p" | grep -o '"patch_hash":"[0-9a-f]*"' | head -1 | cut -d'"' -f4)
    rt=$(printf '%s' "$p" | grep -o '"tree_hash":"[0-9a-f]*"' | head -1 | cut -d'"' -f4)
    printf '{"schema_version":1,"job_id":"%s","attempt_id":"%s","patch_hash":"%s","tree_hash":"%s","status":"passed","findings":[]}\n' "$rj" "$att" "$rp" "$rt" > "$TMP/review.json"
    fake_set_state idle; td tandem build review-record --root "$R" --result "$TMP/review.json" >/dev/null 2>&1
    fake_set_state idle; td tandem build tick --root "$R" >/dev/null 2>&1
  done
}
# old_report DIR: a handback an earlier legacy build left unsigned in the relay.
old_report() { mkdir -p "$1/.tandem-relay/reports"; printf 'an old legacy build\n' > "$1/.tandem-relay/reports/20260930-221811-77503-20677.md"; }
unpair() { (cd "$R" && env PATH="$PATHX" REPO="$R" PANE="w1:p9" TANDEM_UNPAIR_SETTLE_POLLS=3 tandem-unpair "$@" 2>&1); }

R="$TMP/repo4"; mkrepo "$R"; R=$(cd "$R" && pwd -P); : > "$FAKE/prompts"
RUN=$(td env TANDEM_BUILD_ENGINE=durable tandem build start --plan "$R/plan.md" --root "$R" --builder builder --planner planner --builder-kind codex --reviewer reviewer --reviewer-kind claude --check true 2>/dev/null | jq -r .run_id)
finish_tasks 2
ST=$(td tandem build status --root "$R" --json | jq -r '"\(.state) \([.tasks[] | select(.accepted)] | length)"')
[ "$ST" = "finished 2" ] && ok "setup: the run finished with both tasks accepted" || nope "setup: $ST"
old_report "$R"
OUT=$(unpair --dry-run); rc=$?
[ "$rc" -eq 0 ] && ok "a dry run is not refused for an old legacy report" || nope "dry run rc $rc: $OUT"
case "$OUT" in *"NOT signed off"*|*"never handed back"*) nope "the old report or the missing report was gated: $OUT" ;; *) ok "and neither the old report nor a missing report is gated" ;; esac
case "$OUT" in *"20260930-221811-77503-20677"*"not part of"*) ok "the old report is named as not part of the durable run" ;; *) nope "the old report is not explained: $OUT" ;; esac
OUT=$(unpair); rc=$?
[ "$rc" -eq 0 ] && ok "unpair closes the pair (no --verified, no --force)" || nope "unpair rc $rc: $OUT"
case "$OUT" in *"check passed"*) ok "the check still ran" ;; *) nope "the check did not run: $OUT" ;; esac
case "$OUT" in *"archived durable run $RUN"*) ok "and the run was archived" ;; *) nope "no archive: $OUT" ;; esac
[ ! -e "$R/.tandem-relay" ] && ok "the relay is gone" || nope "the relay is still there"

R="$TMP/repo5"; mkrepo "$R"; R=$(cd "$R" && pwd -P); : > "$FAKE/prompts"
RUN=$(td env TANDEM_BUILD_ENGINE=durable tandem build start --plan "$R/plan.md" --root "$R" --builder builder --planner planner --builder-kind codex --reviewer reviewer --reviewer-kind claude --check true 2>/dev/null | jq -r .run_id)
finish_tasks 1
td tandem build stop --root "$R" >/dev/null 2>&1; fake_set_state idle; td tandem build tick --root "$R" >/dev/null 2>&1
BEFORE=$(tree "$R/.tandem-relay")
OUT=$(unpair); rc=$?
[ "$rc" -eq 5 ] && ok "a run with an unaccepted task is refused (exit 5)" || nope "partial run rc $rc: $OUT"
case "$OUT" in *"task-2"*) ok "and the refusal names the task" ;; *) nope "task-2 not named: $OUT" ;; esac
case "$OUT" in *"--verified"*) nope "it asks for --verified on a durable run: $OUT" ;; *) ok "and does not ask for a report sign-off" ;; esac
[ "$BEFORE" = "$(tree "$R/.tandem-relay")" ] && ok "nothing was changed" || nope "the relay changed"

R="$TMP/repo6"; mkrepo "$R"; R=$(cd "$R" && pwd -P); : > "$FAKE/prompts"
td env TANDEM_BUILD_ENGINE=durable tandem build start --plan "$R/plan.md" --root "$R" --builder builder --planner planner --builder-kind codex --reviewer reviewer --reviewer-kind claude --check true >/dev/null 2>&1
td tandem build stop --root "$R" >/dev/null 2>&1; td tandem build tick --root "$R" >/dev/null 2>&1
OUT=$(unpair); rc=$?
[ "$rc" -eq 4 ] && ok "a run with nothing accepted is refused (exit 4)" || nope "empty run rc $rc: $OUT"
case "$OUT" in *"no task"*) ok "and says no task was accepted" ;; *) nope "no reason: $OUT" ;; esac

printf '\n== an unknown engine is refused before anything is written ==\n'
for ver in '"engine_version":99,"protocol_version":99' '"engine_version":2,"protocol_version":3'; do
  R3="$TMP/repo3"; mkrepo "$R3"; R3=$(cd "$R3" && pwd -P); R="$R3"
  mkdir -p "$R3/.tandem-relay/durable/$RUN"
  printf '{%s,"run_id":"%s"}\n' "$ver" "$RUN" > "$R3/.tandem-relay/active-run.json"
  SNAP=$(tree "$R3")
  OUT=$(cd "$R3" && env PATH="$PATHX" REPO="$R3" tandem-unpair --skip-checks 2>&1); rc=$?
  [ "$rc" -eq 23 ] && ok "unpair refuses ($ver) with exit 23" || nope "unpair rc $rc: $OUT"
  case "$OUT" in *"install.sh"*) ok "and names the remedy" ;; *) nope "no remedy: $OUT" ;; esac
  [ "$SNAP" = "$(tree "$R3")" ] && ok "unpair wrote and deleted nothing" || nope "unpair changed the tree"
  OUT=$(cd "$R3" && env PATH="$PATHX" REPO="$R3" tandem-bridge --mailbox "$R3/.tandem-relay" --once 2>&1); rc=$?
  [ "$rc" -eq 23 ] && ok "the bridge refuses with exit 23" || nope "bridge rc $rc: $OUT"
  [ "$SNAP" = "$(tree "$R3")" ] && [ ! -d "$R3/.tandem-relay/q" ] && ok "the bridge created and moved nothing" || nope "the bridge changed the tree"
done

printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

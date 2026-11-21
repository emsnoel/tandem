#!/usr/bin/env bash
# Targeted tests for:
#   - Issue #107: tandem-review runs/caches the baseline check itself for a
#     whole-plan review, includes exit code + summary in the prompt, and tells
#     the reviewer NOT to run the full check.
#   - Issue #112: tandem-review --accepted-tasks scopes the review to remaining
#     tasks, and tandem-handoff --resume passes accepted tasks from the archived
#     run when the review gate requests a fresh review.
set -uo pipefail
. "$(cd "$(dirname "$0")" && pwd)/lib/records.sh"
ROOT_DIR=$(cd "$(dirname "$0")/.." && pwd)

PASS=0; FAIL=0
ok()    { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope()  { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }
has()   { case "$2" in *"$3"*) ok "$1" ;; *) nope "$1 (missing: $3)" ;; esac; }
hasno() { case "$2" in *"$3"*) nope "$1 (unexpected: $3)" ;; *) ok "$1" ;; esac; }

TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
mkdir -p "$TMP/bin"
(cd "$ROOT_DIR" && go build -o "$TMP/tandem-real" ./cmd/tandem) || { echo "could not build tandem"; exit 1; }
printf '#!/bin/sh\necho "jq was called: $*" >&2\nexit 99\n' > "$TMP/bin/jq"
printf '#!/usr/bin/env bash\nexit 0\n' > "$TMP/bin/herdr"
chmod +x "$TMP/bin/jq" "$TMP/bin/herdr"

# Real tandem for gate, config, paths, reviewer, state; stub only pane/socket ops
# so this suite is hermetic even inside sandboxes that deny Unix sockets.
cat > "$TMP/bin/tandem" <<'STUB'
#!/usr/bin/env bash
case "$1 $2" in
  "agent snapshot") printf 'w1:%s\t%s\tidle\n' "$3" "$REPO"; exit 0 ;;
  "agent reset")    printf 'reset\n'; exit 0 ;;
  "agent prompt")   printf '%s' "$4" > "$PROMPT_OUT"; printf 'started\tworking\n'; exit 0 ;;
esac
case "$1" in
  pane) exit 0 ;;
esac
exec "$TANDEM_REAL" "$@"
STUB
chmod +x "$TMP/bin/tandem"

REPO="$TMP/repo"; MB="$REPO/.tandem-relay"
mkdir -p "$REPO/docs/plans" "$MB"
git -C "$REPO" init -q
git -C "$REPO" config user.email t@t
git -C "$REPO" config user.name t
REPO=$(cd "$REPO" && pwd -P); MB="$REPO/.tandem-relay"
printf '.tandem-relay/\n' > "$REPO/.gitignore"
COUNTER="$TMP/check-runs"
printf '{"plans_dir":"docs/plans","check_command":"printf x >> %s && echo baseline-passed"}\n' "$COUNTER" > "$REPO/.tandem.json"
PLAN="$REPO/docs/plans/p.md"
printf '# Plan\n\n**Goal:** g.\n\n### Task 1: First\nfirst\n\n### Task 2: Second\nsecond\n\n### Task 3: Third\nthird\n' > "$PLAN"
git -C "$REPO" add . && git -C "$REPO" commit -qm init
write_builder "$MB" claude
printf '{"kind":"codex","name":"reviewer"}\n' > "$MB/reviewer.json"

PROMPT_OUT="$TMP/prompt.txt"
review() {
  rm -f "$PROMPT_OUT"
  OUT=$(cd "$REPO" && env -u HERDR_DECISION PATH="$TMP/bin:$ROOT_DIR/bin:/usr/bin:/bin" \
        HERDR_ENV=1 TANDEM_REAL="$TMP/tandem-real" PROMPT_OUT="$PROMPT_OUT" REPO="$REPO" \
        tandem-review "$PLAN" "$@" 2>&1); RC=$?
}

printf '== #107: whole-plan review runs baseline check, caches it, and tells reviewer not to run full check ==\n'
review
[ "$RC" -eq 0 ] && ok "first whole-plan review succeeds" || nope "first review rc $RC: $OUT"
P=$(cat "$PROMPT_OUT" 2>/dev/null)
has "logs fresh baseline check" "$OUT" "baseline check: exit 0 (fresh)"
has "prompt tells reviewer not to run the full check" "$P" "do not run the full check command"
has "prompt allows targeted test to probe a claim" "$P" "targeted test to probe a specific"
hasno "prompt does not tell reviewer to run check in one blocking call" "$P" "run the check command once, now, in ONE blocking call"
has "prompt includes baseline check exit" "$P" "exit: 0 (fresh)"
has "prompt includes baseline check summary output" "$P" "baseline-passed"
[ "$(cat "$COUNTER" 2>/dev/null)" = "x" ] && ok "check command ran once" || nope "check runs: $(cat "$COUNTER" 2>/dev/null)"

# Second review on the same working tree reuses cached baseline check.
review
[ "$RC" -eq 0 ] && ok "second whole-plan review succeeds" || nope "second review rc $RC: $OUT"
P=$(cat "$PROMPT_OUT" 2>/dev/null)
has "logs cached baseline check" "$OUT" "baseline check: exit 0 (cached)"
has "prompt notes cached baseline check" "$P" "exit: 0 (cached)"
[ "$(cat "$COUNTER" 2>/dev/null)" = "x" ] && ok "check command was not re-run on unchanged tree" || nope "check runs after cache hit: $(cat "$COUNTER" 2>/dev/null)"

# Modifying the working tree invalidates the cache.
printf 'changed\n' > "$REPO/dirty.txt"
review
[ "$RC" -eq 0 ] && ok "review after tree change succeeds" || nope "dirty review rc $RC: $OUT"
has "logs fresh baseline check after tree change" "$OUT" "baseline check: exit 0 (fresh)"
[ "$(cat "$COUNTER" 2>/dev/null)" = "xx" ] && ok "check command re-ran after tree change" || nope "check runs after edit: $(cat "$COUNTER" 2>/dev/null)"
rm -f "$REPO/dirty.txt"

printf '\n== #112: tandem-review --accepted-tasks scopes review to remaining tasks ==\n'
review --accepted-tasks task-1,task-2 --dry-run
[ "$RC" -eq 0 ] && ok "dry-run with --accepted-tasks succeeds" || nope "dry-run rc $RC: $OUT"
has "logs accepted tasks" "$OUT" "accepted tasks (already done): task-1,task-2"
has "prompt says review is for a resumed run" "$OUT" "This review is for a resumed run"
has "prompt lists accepted task IDs" "$OUT" "task-1, task-2"
has "prompt asks to judge ONLY remaining tasks" "$OUT" "Judge ONLY the remaining (unaccepted) tasks against the current working tree."

printf '\n== #112: tandem-handoff --resume passes accepted tasks to tandem-review ==\n'
HBIN="$TMP/hbin"; mkdir -p "$HBIN"
CALLS="$TMP/review-calls"; : > "$CALLS"
cat > "$HBIN/tandem" <<STUB
#!/usr/bin/env bash
case "\$1 \$2" in
  "agent snapshot")        printf 'w1:%s\t%s\tidle\n' "\$3" "$REPO"; exit 0 ;;
  "gate review")           printf 'builder\ncodex auto\nclaude auto\n20260910-000001-1-1\n'; exit 24 ;;
  "state resume-accepted") printf 'task-1,task-2\n'; exit 0 ;;
esac
exec "$TMP/tandem-real" "\$@"
STUB
cat > "$HBIN/tandem-review" <<STUB
#!/usr/bin/env bash
printf '%s\n' "\$*" >> "$CALLS"
exit 0
STUB
chmod +x "$HBIN/tandem" "$HBIN/tandem-review"

OUT=$(cd "$REPO" && PATH="$HBIN:$TMP/bin:$ROOT_DIR/bin:/usr/bin:/bin" HERDR_ENV=1 TANDEM_REAL="$TMP/tandem-real" REPO="$REPO" \
      tandem-handoff "$PLAN" --resume --dry-run 2>&1); RC=$?
[ "$RC" -eq 24 ] && ok "handoff --resume --dry-run exits 24 at review gate" || nope "dry-run handoff rc $RC: $OUT"
has "dry-run shows --accepted-tasks in planned tandem-review command" "$OUT" "tandem-review $PLAN --accepted-tasks task-1,task-2"

OUT=$(cd "$REPO" && PATH="$HBIN:$TMP/bin:$ROOT_DIR/bin:/usr/bin:/bin" HERDR_ENV=1 TANDEM_REAL="$TMP/tandem-real" REPO="$REPO" \
      tandem-handoff "$PLAN" --resume 2>&1); RC=$?
[ "$RC" -eq 24 ] && ok "handoff --resume exits 24 after requesting review" || nope "handoff rc $RC: $OUT"
has "tandem-review was invoked with --accepted-tasks task-1,task-2" "$(cat "$CALLS")" "$PLAN --accepted-tasks task-1,task-2"

printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

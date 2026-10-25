#!/usr/bin/env bash
# Every review starts in a fresh reviewer conversation, and a later review
# reads only the change since the last one.
set -uo pipefail
. "$(cd "$(dirname "$0")" && pwd)/lib/records.sh"
REVIEW="${1:-$(cd "$(dirname "$0")/.." && pwd)/bin/tandem-review}"
[ -x "$REVIEW" ] || { echo "not executable: $REVIEW" >&2; exit 2; }

PASS=0; FAIL=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }
has()  { case "$2" in *"$3"*) ok "$1" ;; *) nope "$1 (missing: $3)" ;; esac; }
hasno() { case "$2" in *"$3"*) nope "$1 (present: $3)" ;; *) ok "$1" ;; esac; }

TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
. "$(cd "$(dirname "$0")" && pwd)/lib/stub-handoff.sh"
stub_handoff_env "$TMP"
REPO="$TMP/repo"; MB="$REPO/.tandem-relay"; RV="$MB/reviews"
mkdir -p "$REPO/docs/plans" "$RV"
PLAN="$REPO/docs/plans/p.md"
# The review goes to the pair's reviewer.
write_reviewer "$MB" codex
# Class 4 names the builder's kind and autonomy, from builder.json.
write_builder "$MB" claude
ID=20260926-170048-111-222
printf '# plan\n### Task 1: one\nold step\n### Task 2: two\nsame\n' > "$RV/$ID.plan.md"
printf '# plan\n### Task 1: one\nnew step\n### Task 2: two\nsame\n' > "$PLAN"
printf '1. ORDERING - Task 1 needs Task 2.\n' > "$RV/$ID.md"
CALLS="$TMP/calls"; SENT="$TMP/sent"

# The real `tandem agent self --expect` decides whether a handback comes from
# the reviewer pane.
(cd "$(dirname "$0")/.." && go build -o "$TMP/tandem-real" ./cmd/tandem) || { echo "could not build tandem" >&2; exit 2; }
export TANDEM_REAL="$TMP/tandem-real"
cat > "$TMP/bin/tandem.extra" <<'EXTRA'
case "$1 $2" in
  "agent self")    exec "$TANDEM_REAL" "$@" ;;
  "gate scope")    [ -n "${STUB_SCOPE:-}" ] && printf '%b\n' "$STUB_SCOPE"; exit 0 ;;
  "agent snapshot") printf '%%1\t%s\t%s\n' "$REPO" "${STUB_SNAP:-done}"; exit 0 ;;
  "agent reset")
    printf 'reset %s\n' "$*" >> "$CALLS"
    # Like the real command: a dry run decides and sends nothing.
    case " $* " in *" --dry-run "*) printf 'would-reset\n' ;; *) printf '%s\n' "${STUB_RESET:-sent}" ;; esac
    exit 0 ;;
  "agent explain-reset") printf 'SAY %s\n' "$*"; exit 0 ;;
  "agent prompt")
    printf 'prompt\n' >> "$CALLS"; printf '%s' "$4" > "$SENT"
    # --as answers from the status after the prompt; still settled means the
    # prompt never started.
    case " $* " in *" --as "*)
      st=${STUB_STATUS:-working}
      case "$st" in idle|done) printf 'agent_never_started\tthe prompt never started\n' >&2; exit 25 ;; esac
      printf 'started\t%s\n' "$st" ;;
    esac
    exit 0 ;;
  "agent status")  printf '%s\n' "${STUB_STATUS:-working}"; exit 0 ;;
esac
EXTRA

send() {  # send [VAR=value...] -- one real review request
  rm -f "$CALLS" "$SENT" "$RV"/*.diff
  OUT=$(env -u HERDR_DECISION -u HERDR_JUDGE PATH="$TMP/bin:$PATH" HERDR_ENV=1 REPO="$REPO" \
        CALLS="$CALLS" SENT="$SENT" "$@" "$REVIEW" "$PLAN" 2>&1); RC=$?
  PROMPT=$(cat "$SENT" 2>/dev/null)
}
dry() {  # dry [VAR=value...] -- render only
  rm -f "$CALLS" "$SENT" "$RV"/*.diff
  OUT=$(env -u HERDR_DECISION -u HERDR_JUDGE PATH="$TMP/bin:$PATH" HERDR_ENV=1 REPO="$REPO" \
        CALLS="$CALLS" SENT="$SENT" "$@" "$REVIEW" "$PLAN" --dry-run 2>&1); RC=$?
  PROMPT="$OUT"
}
DIFF_SCOPE="diff\n$ID"

printf '== first review: the whole plan ==\n'
dry
has "reads the whole plan" "$PROMPT" "Read the whole plan, then read the code it touches"
has "runs the check once" "$PROMPT" "run the check command once"
has "counts a task that must end red as impossible" "$PROMPT" "stops the build on red"
hasno "is not a re-review" "$PROMPT" "Re-review"
has "says it is a whole-plan review" "$OUT" "review: the whole plan"
has "says what the reset would do, for the review" "$OUT" "SAY agent explain-reset would-reset --for review"

printf '\n== later review: only the change ==\n'
dry STUB_SCOPE="$DIFF_SCOPE"
has "is a re-review" "$PROMPT" "Re-review the plan at $PLAN"
has "names the earlier review" "$PROMPT" "$RV/$ID.md"
has "names the diff" "$PROMPT" "$RV/$ID.next.diff"
has "cites objections by review id" "$PROMPT" "$ID#N"
has "forbids a full re-read" "$PROMPT" "DO NOT read the whole plan again"
hasno "does not run the check" "$PROMPT" "run the check command once"
has "says which review it continues" "$OUT" "review: the changes since review $ID"
[ -e "$RV/$ID.next.diff" ] && nope "dry run writes no diff" || ok "dry run writes no diff"
[ -e "$CALLS" ] && grep -qx prompt "$CALLS" && nope "dry run sends nothing" || ok "dry run sends nothing"

printf '\n== later review, for real ==\n'
send STUB_SCOPE="$DIFF_SCOPE"
[ "$RC" -eq 0 ] && ok "succeeds" || nope "succeeds (rc $RC)"
[ "$(sed -n 1p "$CALLS" | cut -d' ' -f1)" = reset ] && ok "resets before prompting" || nope "resets before prompting ($(tr '\n' '|' < "$CALLS"))"
grep -qx prompt "$CALLS" && ok "then prompts" || nope "then prompts"
grep -q -- '^+new step$' "$RV/$ID.next.diff" 2>/dev/null && ok "diff holds the new line" || nope "diff holds the new line"
grep -q -- '^-old step$' "$RV/$ID.next.diff" 2>/dev/null && ok "diff holds the old line" || nope "diff holds the old line"
has "sent the re-review" "$PROMPT" "Re-review the plan"
has "explains the reset for the review" "$OUT" "SAY agent explain-reset sent --for review"

printf '\n== a diff answer without the earlier files falls back to full ==\n'
dry STUB_SCOPE="diff\n20260101-000000-1-1"
has "warns" "$OUT" "WARNING the previous review's files are missing"
has "reads the whole plan" "$PROMPT" "Read the whole plan, then read the code it touches"

printf '\n== a malformed id is never used in a path ==\n'
dry STUB_SCOPE="diff\n../../etc/passwd"
has "warns" "$OUT" "WARNING the previous review's files are missing"
hasno "no traversal in the prompt" "$PROMPT" "etc/passwd"

printf '\n== an older tandem without gate scope reviews in full ==\n'
dry STUB_SCOPE=
has "reads the whole plan" "$PROMPT" "Read the whole plan, then read the code it touches"

printf '\n== delivery from a done reviewer ==\n'
send STUB_SNAP=done STUB_STATUS=working
[ "$RC" -eq 0 ] && ok "confirmed once it starts working" || nope "confirmed once it starts working (rc $RC)"
has "says so" "$OUT" "review requested (reviewer now working)"
# The stub answers --as at once, like tests/test-handoff-reset.sh.
send STUB_SNAP=done STUB_STATUS=done
[ "$RC" -eq 7 ] && ok "a reviewer that stays done never took it (exit 7)" || nope "a reviewer that stays done never took it (rc $RC)"

printf '\n== an unconfirmed reset is never typed into (exit 7) ==\n'
# Unconfirmed: the pane may be at a dialog that would swallow the prompt.
# Send nothing.
send STUB_SNAP=idle STUB_RESET=unconfirmed STUB_STATUS=working
[ "$RC" -eq 7 ] && ok "exits 7" || nope "exits 7 (rc $RC)"
grep -qx prompt "$CALLS" && nope "sends no prompt" || ok "sends no prompt"
has "names the pane to inspect" "$OUT" "Check pane"
hasno "does not claim the review was requested" "$OUT" "review requested"

printf '\n== repo text cannot be rewritten by a later slot ==\n'
# check_command is repo-controlled, so it is substituted last.
dry STUB_CHECK='make {{PREV}} test'
has "check command arrives verbatim" "$PROMPT" "make {{PREV}} test"

printf '\n== a build task is reviewed by the reviewer pane, never the builder ==\n'
# The legacy build loop types the review into the reviewer pane and the
# verdict returns through tandem-handback. One from the builder's pane is
# refused before anything is queued.
HANDBACK="$(cd "$(dirname "$REVIEW")" && pwd)/tandem-handback"
mkdir -p "$MB/build"
printf '{"state":"running","builder":"builder","reviewer":"reviewer","current":1,"tasks":[{"status":"reviewing"}]}\n' > "$MB/build/build.json"
printf 'looks fine\n' > "$TMP/findings.md"
handback() {  # handback [VAR=value...] -- one review handback of task 1
  rm -rf "$MB/q"
  OUT=$(cd "$REPO" && env -u HERDR_ROLE -u HERDR_PANE_ID PATH="$TMP/bin:$PATH" HERDR_ENV=1 REPO="$REPO" "$@" \
        "$HANDBACK" --task 1 --review --verdict clean --report "$TMP/findings.md" 2>&1); RC=$?
}
handback HERDR_ROLE=builder
[ "$RC" -ne 0 ] && ok "the builder's review handback is refused (rc $RC)" || nope "the builder's review handback is refused"
has "says the builder never reviews" "$OUT" "The builder never reviews"
has "says nothing was sent" "$OUT" "NOTHING WAS SENT"
[ ! -d "$MB/q" ] && ok "nothing is queued for the bridge" || nope "nothing is queued for the bridge"
[ "$(jq -r '.tasks[0].status' "$MB/build/build.json")" = reviewing ] && ok "the task stays in review" || nope "the task stays in review"
handback
[ "$RC" -ne 0 ] && ok "a pane that cannot name itself is not assumed to be the reviewer" || nope "an unnamed pane was taken for the reviewer"
handback HERDR_ROLE=reviewer
[ "$RC" -eq 0 ] && jq -e '.review==true and .verdict=="clean" and .from=="reviewer"' "$MB"/q/*.json >/dev/null 2>&1 \
  && ok "the reviewer pane's verdict is queued with its name" || nope "the reviewer's verdict was not queued (rc $RC): $OUT"

printf '\n%s passed, %s failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

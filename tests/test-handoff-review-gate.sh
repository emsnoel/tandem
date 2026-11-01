#!/usr/bin/env bash
# An unreviewed plan must never reach the builder.
#
# Every tandem call is logged, so the test proves no `agent prompt` follows a
# refusal. The branch that requests a review does send a review prompt and
# must not claim otherwise.
#
# Hermetic: tandem is stubbed, so no server or panes are needed.
set -uo pipefail
. "$(cd "$(dirname "$0")" && pwd)/lib/records.sh"
HANDOFF="${1:-$(cd "$(dirname "$0")/.." && pwd)/bin/tandem-handoff}"
[ -x "$HANDOFF" ] || { echo "not executable: $HANDOFF" >&2; exit 2; }

PASS=0; FAIL=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }
chk()  { case "$OUT" in *"$2"*) ok "$1" ;; *) nope "$1 (missing: $2)" ;; esac; }
nchk() { case "$OUT" in *"$2"*) nope "$1 (unexpected: $2)" ;; *) ok "$1" ;; esac; }

TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
mkdir -p "$TMP/bin" "$TMP/repo/docs/plans"
PLAN="$TMP/repo/docs/plans/p.md"; printf '# plan\n' > "$PLAN"

# The stub logs every call, answers the reads tandem-handoff makes before the
# gate, and reports objections from `gate review`.
cat > "$TMP/bin/tandem" <<'STUB'
#!/usr/bin/env bash
. "$(dirname "$0")/stub-common"
printf '%s\n' "$*" >> "$MARKER"
case "$1 $2" in
  "agent snapshot") printf '%%1\t%s\tidle\n' "$REPO"; exit 0 ;;
  "gate review")    printf 'objections\n20260910-000001-1-1\n'; exit 24 ;;
esac
case "$1" in
  config) printf '{"kind":{"value":"codex"},"autonomy":{"value":"auto"}}\n'; exit 0 ;;
  paths)  printf '%s/.tandem-relay\n' "$REPO"; exit 0 ;;
esac
exit 0
STUB
# stub-common answers what every stub below answers the same way. build start
# is canned. config project reads check_command and prompt_extra from the
# check-command and prompt-extra files beside it. The real binary reads and
# writes the review sidecar.
cat > "$TMP/bin/stub-common" <<'STUB'
nul() { local f; for f in "$@"; do printf '%s\0' "$f"; done; }
_here=$(dirname "$0")
case "$1 $2" in
  # engine run_id dry_run task total title brief ledger resumed replaced judge
  # reviewer review_pending review_prompt
  "build start") nul "" "" "" 1 1 "the plan" /relay/build/task-1.md "" "" "" "" "" "" ""; exit 0 ;;
  # root plans_dir check_command conventions_file builder_kind prompt_extra
  # task_input_budget context_limit build_engine review_delivery
  # acceptance_policy collaboration source invalid_manifest
  "config project")
    nul "$REPO" docs/plans "$(cat "$_here/check-command")" "" "" "$(cat "$_here/prompt-extra")" "" "" "" "" "" "" stub ""
    exit 0 ;;
  "config builder-kind") printf 'codex\n'; exit 0 ;;
  "gate ruling"|"gate rule") exec "$REAL_TANDEM" "$@" ;;
esac
STUB
# project CHECK EXTRA: the project's check_command and prompt_extra.
project() { printf '%s' "$1" > "$TMP/bin/check-command"; printf '%s' "$2" > "$TMP/bin/prompt-extra"; }
project true ""
export REAL_TANDEM; REAL_TANDEM=$(command -v tandem) || { echo "need an installed tandem" >&2; exit 2; }
# herdr is stubbed. The tests read sidecars with the real jq; tandem-handoff
# gets one that fails, since it must call none.
command -v jq >/dev/null || { echo "need jq" >&2; exit 2; }
export REAL_JQ; REAL_JQ=$(command -v jq)
printf '#!/bin/sh\necho "jq was called: $*" >&2\nexit 99\n' > "$TMP/bin/jq"
printf '#!/usr/bin/env bash\nexit 0\n' > "$TMP/bin/herdr"
# Logged, so the test can tell a requested review from a printed command.
cat > "$TMP/bin/tandem-review" <<'STUB'
#!/usr/bin/env bash
printf 'tandem-review %s\n' "$*" >> "$MARKER"
exit 0
STUB
chmod +x "$TMP/bin"/*

MARKER="$TMP/calls"; : > "$MARKER"
RC=0
OUT=$(PATH="$TMP/bin:$PATH" HERDR_ENV=1 MARKER="$MARKER" REPO="$TMP/repo" \
        "$HANDOFF" "$PLAN" 2>&1) || RC=$?

case "$RC" in
  24) ok "a plan with standing objections exits 24" ;;
  *)  nope "expected rc 24, got $RC" ;;
esac
chk "it says the plan was not delivered" "THE PLAN WAS NOT DELIVERED"
chk "it says nothing was sent"        "Nothing was sent"
chk "it names the review id"          "20260910-000001-1-1"
# The plan fix is the cheaper remedy, so it must come before the override.
case "$OUT" in
  *"Fix the plan"*"--override-review"*)
    ok "it offers the plan fix first" ;;
  *"--override-review"*"Fix the plan"*)
    nope "it offers the plan fix first (override appears before the fix)" ;;
  *)
    nope "it offers the plan fix first (missing: Fix the plan or --override-review)" ;;
esac
chk "it offers the override"          "--override-review"
chk "the override remedy needs a why" "--because"

if grep -q 'agent prompt' "$MARKER"; then
  nope "the builder WAS prompted despite the refusal"
else
  ok "the builder was never prompted"
fi

# Standing objections need a decision, not another review.
if grep -q 'tandem-review' "$MARKER"; then
  nope "objections triggered an automatic re-review"
else
  ok "objections refuse rather than re-reviewing"
fi

# --- no current review: handoff requests one ---
cat > "$TMP/bin/tandem" <<'STUB'
#!/usr/bin/env bash
. "$(dirname "$0")/stub-common"
printf '%s\n' "$*" >> "$MARKER"
case "$1 $2" in
  "agent snapshot") printf '%%1\t%s\tidle\n' "$REPO"; exit 0 ;;
  "gate review")    printf 'absent\n'; exit 24 ;;
esac
case "$1" in
  config) printf '{"kind":{"value":"codex"},"autonomy":{"value":"auto"}}\n'; exit 0 ;;
  paths)  printf '%s/.tandem-relay\n' "$REPO"; exit 0 ;;
esac
exit 0
STUB
chmod +x "$TMP/bin/tandem"

: > "$MARKER"
RC=0
OUT=$(PATH="$TMP/bin:$PATH" HERDR_ENV=1 MARKER="$MARKER" REPO="$TMP/repo" \
        "$HANDOFF" "$PLAN" 2>&1) || RC=$?

case "$RC" in
  24) ok "an unreviewed plan still exits 24" ;;
  *)  nope "expected rc 24 for an unreviewed plan, got $RC" ;;
esac
chk "it says the plan was not delivered" "THE PLAN WAS NOT DELIVERED"
chk "it says no review exists"     "No review exists"
chk "it distinguishes reading from building" "not to build it"
case "$OUT" in
  *"othing was sent"*) nope "it claims nothing was sent while requesting a review" ;;
  *)                   ok   "it does not claim nothing was sent" ;;
esac
chk "it says it is requesting one" "Requesting a review"
chk "it says the verdict will wake you" "wake you with its verdict"
if grep -q "tandem-review $PLAN" "$MARKER"; then
  ok "it ran tandem-review with this plan"
else
  nope "it did not run tandem-review"
fi
if grep -q 'agent prompt' "$MARKER"; then
  nope "the plan was handed over despite having no review"
else
  ok "the plan itself was never sent"
fi

# --dry-run names the action without using a builder turn.
: > "$MARKER"
RC=0
OUT=$(PATH="$TMP/bin:$PATH" HERDR_ENV=1 MARKER="$MARKER" REPO="$TMP/repo" \
        "$HANDOFF" "$PLAN" --dry-run 2>&1) || RC=$?
chk "a dry run names the review it would request" "[dry-run] would run: tandem-review"
if grep -q 'tandem-review' "$MARKER"; then
  nope "a dry run actually requested a review"
else
  ok "a dry run requested nothing"
fi

# --- the override path ---
# The stub now passes the gate, so --dry-run renders the real launch prompt.
cat > "$TMP/bin/tandem" <<'STUB'
#!/usr/bin/env bash
. "$(dirname "$0")/stub-common"
printf '%s\n' "$*" >> "$MARKER"
case "$1 $2" in
  "agent snapshot") printf '%%1\t%s\tidle\n' "$REPO"; exit 0 ;;
  "gate review")    printf '20260910-000001-1-1\n'; exit 0 ;;
esac
case "$1" in
  config) printf '{"kind":{"value":"codex"},"autonomy":{"value":"auto"}}\n'; exit 0 ;;
  paths)  printf '%s/.tandem-relay\n' "$REPO"; exit 0 ;;
esac
exit 0
STUB
printf '#!/usr/bin/env bash\nexit 0\n' > "$TMP/bin/tandem-review"
chmod +x "$TMP/bin/tandem" "$TMP/bin/tandem-review"
mkdir -p "$TMP/repo/.tandem-relay/reviews"
printf '#1 ordering - task 4 deletes h(), task 5 introduces it\n' \
  > "$TMP/repo/.tandem-relay/reviews/20260910-000001-1-1.md"
# The ruling is recorded into the sidecar, so one must exist.
printf '{"plan":"%s","verdict":"objections","kind":"codex","autonomy":"auto","tree":"","override_reason":""}\n' \
  "$PLAN" > "$TMP/repo/.tandem-relay/reviews/20260910-000001-1-1.json"

RC=0
OUT=$(PATH="$TMP/bin:$PATH" HERDR_ENV=1 MARKER="$MARKER" REPO="$TMP/repo" \
        "$HANDOFF" "$PLAN" --dry-run \
        --override-review 20260910-000001-1-1 \
        --because 'I commit here, not you' 2>&1) || RC=$?

chk "the prompt carries the overruled block" "OVERRULED OBJECTIONS"
# The pre-check also prints the reason, so require it after the OVERRULED
# block's marker to prove the block rendered.
case "$OUT" in
  *"OVERRULED OBJECTIONS"*"I commit here, not you"*)
    ok "it carries the ruling" ;;
  *)
    nope "it carries the ruling (not found inside the rendered OVERRULED block)" ;;
esac
chk "it carries the objection beside it"     "task 4 deletes h()"
chk "the ruling outranks the plan"           "outrank the plan"

# --- injection: a --because reason containing {{EXTRA}} reaches the prompt
# unchanged. prompt_extra must be non-empty and distinct for this to test
# anything.
project 'true' 'REPO-EXTRA-MARKER'

RC=0
OUT=$(PATH="$TMP/bin:$PATH" HERDR_ENV=1 MARKER="$MARKER" REPO="$TMP/repo" \
        "$HANDOFF" "$PLAN" --dry-run \
        --override-review 20260910-000001-1-1 \
        --because 'use {{EXTRA}} carefully' 2>&1) || RC=$?
chk "a reason with a literal {{EXTRA}} slot reaches the prompt unchanged" "use {{EXTRA}} carefully"
case "$OUT" in
  *"use REPO-EXTRA-MARKER carefully"*)
    nope "the reason's literal {{EXTRA}} text was rewritten by the repo's prompt_extra" ;;
  *)
    ok "the reason's literal {{EXTRA}} text was not rewritten" ;;
esac

# --- injection: prompt_extra containing {{OVERRULED}} must not pull in the
# ruling.
project 'true' 'see {{OVERRULED}} above'

RC=0
OUT=$(PATH="$TMP/bin:$PATH" HERDR_ENV=1 MARKER="$MARKER" REPO="$TMP/repo" \
        "$HANDOFF" "$PLAN" --dry-run \
        --override-review 20260910-000001-1-1 \
        --because 'I commit here, not you' 2>&1) || RC=$?
# An un-defanged marker would also vanish, replaced by a second copy of the
# ruling, so count the ruling in the rendered prompt only.
_prompt_only=$(printf '%s\n' "$OUT" | sed -n '/--- prompt that would be sent ---/,/--- end ---/p')
_count=$(printf '%s' "$_prompt_only" | grep -o 'I commit here, not you' | wc -l | tr -d ' ')
case "$_count" in
  1) ok "the ruling appears exactly once in the rendered prompt (not duplicated by an un-defanged {{OVERRULED}})" ;;
  *) nope "the ruling appears $_count time(s) in the rendered prompt, want exactly 1" ;;
esac
# The defanged text must have no stray backslash.
case "$OUT" in
  *'\{OVERRULED}'*)
    nope "the defanged text carries a stray backslash the builder would see" ;;
  *'{OVERRULED}'*)
    ok "the defanged text renders as a clean single-brace look-alike" ;;
  *)
    nope "the defanged text did not render as expected" ;;
esac

# Restore the default project stub for the remaining cases.
project 'true' ''

RC=0
OUT=$(PATH="$TMP/bin:$PATH" HERDR_ENV=1 MARKER="$MARKER" REPO="$TMP/repo" \
        "$HANDOFF" "$PLAN" --dry-run 2>&1) || RC=$?
case "$OUT" in
  *OVERRULED*) nope "a clean handoff must not mention overruled objections" ;;
  *)           ok   "a clean handoff carries no overruled block" ;;
esac

# --- the ruling survives a re-handoff of the same plan ---
# The ruling is stored in the sidecar. On a later handoff without
# --override-review, the OVERRULED block must still render from it.
: > "$MARKER"
printf '{"plan":"%s","verdict":"objections","kind":"codex","autonomy":"auto","tree":"","override_reason":"I commit here, not you"}\n' \
  "$PLAN" > "$TMP/repo/.tandem-relay/reviews/20260910-000001-1-1.json"

RC=0
OUT=$(PATH="$TMP/bin:$PATH" HERDR_ENV=1 MARKER="$MARKER" REPO="$TMP/repo" \
        "$HANDOFF" "$PLAN" --dry-run 2>&1) || RC=$?
case "$RC" in
  0) ok "a re-handoff on a persisted ruling is allowed" ;;
  *) nope "expected rc 0 for a re-handoff on a persisted ruling, got $RC" ;;
esac
chk "the re-handoff still carries the overruled block" "OVERRULED OBJECTIONS"
# The ruling must appear after the block marker.
case "$OUT" in
  *"OVERRULED OBJECTIONS"*"I commit here, not you"*)
    ok "the re-handoff carries the persisted ruling" ;;
  *)
    nope "the re-handoff dropped the persisted ruling" ;;
esac
chk "the re-handoff carries the objection beside it" "task 4 deletes h()"
# Nothing changes the stored ruling, so the pre-check must not run.
if grep -q -- '--expect-review' "$MARKER"; then
  nope "a re-handoff ran the override pre-check it was never asked for"
else
  ok "a re-handoff writes nothing"
fi

# --- a ruling stored against a clean review is not rendered ---
# The gate ignores override_reason for a clean verdict, so rendering it would
# hand the builder its own "no objections" text as a ruling. Older sidecars can
# hold one.
: > "$MARKER"
printf 'The plan is fine. No objections.\n' \
  > "$TMP/repo/.tandem-relay/reviews/20260910-000001-1-1.md"
printf '{"plan":"%s","verdict":"clean","kind":"codex","autonomy":"auto","tree":"","override_reason":"just do it"}\n' \
  "$PLAN" > "$TMP/repo/.tandem-relay/reviews/20260910-000001-1-1.json"

RC=0
OUT=$(PATH="$TMP/bin:$PATH" HERDR_ENV=1 MARKER="$MARKER" REPO="$TMP/repo" \
        "$HANDOFF" "$PLAN" --dry-run 2>&1) || RC=$?
case "$RC" in
  0) ok "a clean review with a stale ruling still hands over" ;;
  *) nope "expected rc 0 for a clean review, got $RC" ;;
esac
nchk "a clean review renders no overruled block" "OVERRULED OBJECTIONS"
nchk "the builder is not handed its own prose as an objection" "The plan is fine. No objections."

# --- overruling a clean review records nothing ---
: > "$MARKER"
printf '{"plan":"%s","verdict":"clean","kind":"codex","autonomy":"auto","tree":"","override_reason":""}\n' \
  "$PLAN" > "$TMP/repo/.tandem-relay/reviews/20260910-000001-1-1.json"
RC=0
OUT=$(PATH="$TMP/bin:$PATH" HERDR_ENV=1 MARKER="$MARKER" REPO="$TMP/repo" \
        "$HANDOFF" "$PLAN" --override-review 20260910-000001-1-1 \
        --because 'just do it' 2>&1) || RC=$?
case "$RC" in
  0) ok "overruling a clean review still hands the plan over" ;;
  *) nope "expected rc 0 overruling a clean review, got $RC" ;;
esac
chk "it says there is nothing to overrule" "raised no objections"
chk "it says the ruling was not recorded"  "NOT recorded"
_written=$(jq -r '.override_reason // ""' \
  "$TMP/repo/.tandem-relay/reviews/20260910-000001-1-1.json" 2>/dev/null)
case "$_written" in
  "") ok "nothing was written to the clean review's sidecar" ;;
  *)  nope "a ruling was recorded against a clean review: $_written" ;;
esac

RC=0
OUT=$(PATH="$TMP/bin:$PATH" HERDR_ENV=1 MARKER="$MARKER" REPO="$TMP/repo" \
        "$HANDOFF" "$PLAN" --override-review 20260910-000001-1-1 2>&1) || RC=$?
case "$RC" in
  1) ok "--override-review without --because is refused" ;;
  *) nope "expected rc 1 for a missing --because, got $RC" ;;
esac

# A malformed id must be rejected with its expected shape before it is used
# in a path.
: > "$MARKER"
RC=0
OUT=$(PATH="$TMP/bin:$PATH" HERDR_ENV=1 MARKER="$MARKER" REPO="$TMP/repo" \
        "$HANDOFF" "$PLAN" --override-review '../../etc/passwd' \
        --because 'nope' 2>&1) || RC=$?
case "$RC" in
  1) ok "a malformed --override-review id is refused" ;;
  *) nope "expected rc 1 for a malformed review id, got $RC" ;;
esac
chk "it says what a review id looks like" "20260913-101530-4821-19273"
if grep -q 'gate review' "$MARKER"; then
  nope "it reached the gate with a malformed id"
else
  ok "it refused before reaching the gate"
fi

# An override of a superseded review must refuse before writing, since the
# gate only reads the newest record.
cat > "$TMP/bin/tandem" <<'STUB'
#!/usr/bin/env bash
. "$(dirname "$0")/stub-common"
printf '%s\n' "$*" >> "$MARKER"
case "$1 $2" in
  "agent snapshot") printf '%%1\t%s\tidle\n' "$REPO"; exit 0 ;;
  "gate review")    printf 'superseded\n20260910-000099-1-1\n'; exit 24 ;;
esac
case "$1" in
  paths) printf '%s/.tandem-relay\n' "$REPO"; exit 0 ;;
esac
exit 0
STUB
chmod +x "$TMP/bin/tandem"
BEFORE=$(cat "$TMP/repo/.tandem-relay/reviews/20260910-000001-1-1.json")
RC=0
OUT=$(PATH="$TMP/bin:$PATH" HERDR_ENV=1 MARKER="$MARKER" REPO="$TMP/repo" \
        "$HANDOFF" "$PLAN" --override-review 20260910-000001-1-1 \
        --because 'ignore the commit steps' 2>&1) || RC=$?
case "$RC" in
  24) ok "a superseded override exits 24" ;;
  *)  nope "expected rc 24 for a superseded override, got $RC" ;;
esac
chk "it names the newer review"   "20260910-000099-1-1"
chk "it says the reason was not recorded" "NOT recorded"
if [ "$(cat "$TMP/repo/.tandem-relay/reviews/20260910-000001-1-1.json")" = "$BEFORE" ]; then
  ok "the reason was not written to the superseded record"
else
  nope "the reason WAS written to a record the gate will not read"
fi

# --- --dry-run --override-review does not refuse ---
# The pre-check and the main gate call both read the sidecar, which --dry-run
# leaves unwritten. This stub reads the real override_reason and verdict from
# the sidecar, so the main call must be skipped for the override to render.
DRYID="20260910-000050-1-1"
printf 'objection: do not do X\n' > "$TMP/repo/.tandem-relay/reviews/$DRYID.md"
# "keep" is a key handoff knows nothing about; a recorded ruling must keep it.
printf '{"plan":"%s","verdict":"objections","kind":"codex","autonomy":"auto","override_reason":"","keep":{"a":[1]}}\n' \
  "$PLAN" > "$TMP/repo/.tandem-relay/reviews/$DRYID.json"

cat > "$TMP/bin/tandem" <<'STUB'
#!/usr/bin/env bash
. "$(dirname "$0")/stub-common"
printf '%s\n' "$*" >> "$MARKER"
case "$1 $2" in
  "agent snapshot") printf '%%1\t%s\tidle\n' "$REPO"; exit 0 ;;
  "gate review")
    shift 2
    expect=""
    while (( $# )); do
      case "$1" in
        --expect-review) expect="$2"; shift 2 ;;
        *) shift ;;
      esac
    done
    meta="$REPO/.tandem-relay/reviews/$DRYID.json"
    reason=$("$REAL_JQ" -r '.override_reason // ""' "$meta" 2>/dev/null)
    verdict=$("$REAL_JQ" -r '.verdict // ""' "$meta" 2>/dev/null)
    if [[ -n "$expect" && "$expect" != "$DRYID" ]]; then
      printf 'superseded\n%s\n' "$DRYID"
      exit 24
    fi
    if [[ "$verdict" == clean || -n "$reason" ]]; then
      printf '%s\n' "$DRYID"
      exit 0
    fi
    printf 'objections\n%s\n' "$DRYID"
    exit 24
    ;;
esac
case "$1" in
  config) printf '{"kind":{"value":"codex"},"autonomy":{"value":"auto"}}\n'; exit 0 ;;
  paths)  printf '%s/.tandem-relay\n' "$REPO"; exit 0 ;;
esac
exit 0
STUB
chmod +x "$TMP/bin/tandem"

RC=0
OUT=$(PATH="$TMP/bin:$PATH" HERDR_ENV=1 MARKER="$MARKER" REPO="$TMP/repo" DRYID="$DRYID" \
        "$HANDOFF" "$PLAN" --dry-run \
        --override-review "$DRYID" \
        --because 'dry run must still send this' 2>&1) || RC=$?

case "$RC" in
  0) ok "a dry-run override against real objections exits 0 (would proceed)" ;;
  *) nope "a dry-run override against real objections should exit 0, got $RC (finding 3 regression)" ;;
esac
case "$OUT" in
  *"OVERRULED OBJECTIONS"*"dry run must still send this"*)
    ok "the OVERRULED block renders under --dry-run despite the gate reporting objections" ;;
  *)
    nope "the OVERRULED block did not render (finding 3 regression)" ;;
esac
STORED=$(jq -r '.override_reason' "$TMP/repo/.tandem-relay/reviews/$DRYID.json")
if [ -z "$STORED" ]; then
  ok "dry-run wrote nothing to the sidecar"
else
  nope "dry-run wrote '$STORED' to the sidecar"
fi

# Without --dry-run the ruling is recorded, and every other key stays.
RC=0
OUT=$(PATH="$TMP/bin:$PATH" HERDR_ENV=1 MARKER="$MARKER" REPO="$TMP/repo" DRYID="$DRYID" \
        "$HANDOFF" "$PLAN" --override-review "$DRYID" --because 'record this one' 2>&1) || RC=$?
STORED=$(jq -r '.override_reason' "$TMP/repo/.tandem-relay/reviews/$DRYID.json")
[ "$STORED" = "record this one" ] && ok "a real override records the ruling" || nope "the ruling was not recorded (got '$STORED', rc $RC)"
[ "$(jq -c '.keep' "$TMP/repo/.tandem-relay/reviews/$DRYID.json")" = '{"a":[1]}' ] \
  && ok "and keeps the keys handoff does not know" || nope "the ruling write dropped or changed another key"

# --- an unrecognised pre-check result writes nothing ---
# A tandem too old for --expect-review exits 2 with empty stdout, which looks
# like a bare pass by reason alone. The exit code must decide.
UNRID="20260910-000060-1-1"
printf 'objection: do not do Y\n' > "$TMP/repo/.tandem-relay/reviews/$UNRID.md"
printf '{"plan":"%s","verdict":"objections","kind":"codex","autonomy":"auto","override_reason":""}\n' \
  "$PLAN" > "$TMP/repo/.tandem-relay/reviews/$UNRID.json"

cat > "$TMP/bin/tandem" <<'STUB'
#!/usr/bin/env bash
. "$(dirname "$0")/stub-common"
printf '%s\n' "$*" >> "$MARKER"
case "$1 $2" in
  "agent snapshot") printf '%%1\t%s\tidle\n' "$REPO"; exit 0 ;;
  "gate review")
    shift 2
    for a in "$@"; do
      case "$a" in --expect-review) exit 2 ;; esac
    done
    printf 'objections\n%s\n' "$UNRID"
    exit 24
    ;;
esac
case "$1" in
  config) printf '{"kind":{"value":"codex"},"autonomy":{"value":"auto"}}\n'; exit 0 ;;
  paths)  printf '%s/.tandem-relay\n' "$REPO"; exit 0 ;;
esac
exit 0
STUB
chmod +x "$TMP/bin/tandem"

RC=0
OUT=$(PATH="$TMP/bin:$PATH" HERDR_ENV=1 MARKER="$MARKER" REPO="$TMP/repo" UNRID="$UNRID" \
        "$HANDOFF" "$PLAN" \
        --override-review "$UNRID" \
        --because 'this must not be recorded' 2>&1) || RC=$?

chk "it says the outcome is not understood" "does not"
chk "it says the outcome is not understood (rc)" "rc 2"
STORED=$(jq -r '.override_reason' "$TMP/repo/.tandem-relay/reviews/$UNRID.json")
if [ -z "$STORED" ]; then
  ok "an unrecognised pre-check outcome (stale binary, rc 2) did not write the ruling"
else
  nope "the ruling WAS written despite an unrecognised pre-check outcome (got: $STORED)"
fi

# --- a slot marker in check_command does not corrupt the render ---
# check_command is repo-controlled and substitutes before OVERRULED.
project 'make test {{OVERRULED}}' ''

cat > "$TMP/bin/tandem" <<'STUB'
#!/usr/bin/env bash
. "$(dirname "$0")/stub-common"
printf '%s\n' "$*" >> "$MARKER"
case "$1 $2" in
  "agent snapshot") printf '%%1\t%s\tidle\n' "$REPO"; exit 0 ;;
  "gate review")    printf '20260910-000001-1-1\n'; exit 0 ;;
esac
case "$1" in
  config) printf '{"kind":{"value":"codex"},"autonomy":{"value":"auto"}}\n'; exit 0 ;;
  paths)  printf '%s/.tandem-relay\n' "$REPO"; exit 0 ;;
esac
exit 0
STUB
chmod +x "$TMP/bin/tandem"

RC=0
OUT=$(PATH="$TMP/bin:$PATH" HERDR_ENV=1 MARKER="$MARKER" REPO="$TMP/repo" \
        "$HANDOFF" "$PLAN" --dry-run \
        --override-review 20260910-000001-1-1 \
        --because 'check-command ruling' 2>&1) || RC=$?

_prompt_only=$(printf '%s\n' "$OUT" | sed -n '/--- prompt that would be sent ---/,/--- end ---/p')
_count=$(printf '%s' "$_prompt_only" | grep -o 'check-command ruling' | wc -l | tr -d ' ')
case "$_count" in
  1) ok "a slot marker in check_command does not duplicate the ruling" ;;
  *) nope "the ruling appears $_count time(s) via check_command's {{OVERRULED}} slot (want 1, finding 2 regression)" ;;
esac
case "$_prompt_only" in
  *'make test {OVERRULED}'*)
    ok "check_command's slot marker renders defanged rather than corrupted" ;;
  *)
    nope "check_command's rendered CHECKLINE was not the expected defanged text (finding 2 regression)" ;;
esac

# --- a triple-brace marker is fully defanged ---
# One pass turns "{{{OVERRULED}}}" into a live "{{OVERRULED}}".
project 'make test {{{OVERRULED}}}' ''

cat > "$TMP/bin/tandem" <<'STUB'
#!/usr/bin/env bash
. "$(dirname "$0")/stub-common"
printf '%s\n' "$*" >> "$MARKER"
case "$1 $2" in
  "agent snapshot") printf '%%1\t%s\tidle\n' "$REPO"; exit 0 ;;
  "gate review")    printf '20260910-000001-1-1\n'; exit 0 ;;
esac
case "$1" in
  config) printf '{"kind":{"value":"codex"},"autonomy":{"value":"auto"}}\n'; exit 0 ;;
  paths)  printf '%s/.tandem-relay\n' "$REPO"; exit 0 ;;
esac
exit 0
STUB
chmod +x "$TMP/bin/tandem"

RC=0
OUT=$(PATH="$TMP/bin:$PATH" HERDR_ENV=1 MARKER="$MARKER" REPO="$TMP/repo" \
        "$HANDOFF" "$PLAN" --dry-run \
        --override-review 20260910-000001-1-1 \
        --because 'triple-brace ruling' 2>&1) || RC=$?

_prompt_only=$(printf '%s\n' "$OUT" | sed -n '/--- prompt that would be sent ---/,/--- end ---/p')
_count=$(printf '%s' "$_prompt_only" | grep -o 'triple-brace ruling' | wc -l | tr -d ' ')
case "$_count" in
  1) ok "a TRIPLE-brace slot marker does not duplicate the ruling (one pass is not enough)" ;;
  *) nope "the ruling appears $_count time(s) via a triple-brace marker (want 1, finding 2 nested regression)" ;;
esac
case "$_prompt_only" in
  *'{{OVERRULED}}'*|*'{{{OVERRULED}}}'*)
    nope "a live or partially-defanged slot marker survived into the rendered prompt" ;;
  *'make test {OVERRULED}'*)
    ok "a triple-brace slot marker is fully defanged to the same single-brace form" ;;
  *)
    nope "check_command's rendered CHECKLINE was not the expected fully-defanged text" ;;
esac

# --- a malformed builder.json does not abort the refusal ---
# The gate prints "?" for an unreadable side, and the script must still exit 24.
BADID="20260910-000070-1-1"
printf 'objection: mismatched identity\n' > "$TMP/repo/.tandem-relay/reviews/$BADID.md"
printf '{"plan":"%s","verdict":"clean","kind":"codex","autonomy":"auto","override_reason":""}\n' \
  "$PLAN" > "$TMP/repo/.tandem-relay/reviews/$BADID.json"
printf 'not json at all' > "$TMP/repo/.tandem-relay/builder.json"

project 'true' ''

cat > "$TMP/bin/tandem" <<'STUB'
#!/usr/bin/env bash
. "$(dirname "$0")/stub-common"
printf '%s\n' "$*" >> "$MARKER"
case "$1 $2" in
  "agent snapshot") printf '%%1\t%s\tidle\n' "$REPO"; exit 0 ;;
  "gate review")    printf 'builder\ncodex/auto\n?/?\n%s\n' "$BADID"; exit 24 ;;
esac
case "$1" in
  paths) printf '%s/.tandem-relay\n' "$REPO"; exit 0 ;;
esac
exit 0
STUB
printf '#!/usr/bin/env bash\nexit 0\n' > "$TMP/bin/tandem-review"
chmod +x "$TMP/bin/tandem" "$TMP/bin/tandem-review"

RC=0
OUT=$(PATH="$TMP/bin:$PATH" HERDR_ENV=1 MARKER="$MARKER" REPO="$TMP/repo" BADID="$BADID" \
        "$HANDOFF" "$PLAN" 2>&1) || RC=$?

case "$RC" in
  24) ok "a builder-identity mismatch with a malformed builder.json still exits 24 (not aborted by jq)" ;;
  *)  nope "expected rc 24 with a malformed builder.json, got $RC (regression A: jq abort under set -e)" ;;
esac
chk "it still names the review-was-made-against identity" "reviewed as: codex/auto"
chk "it falls back to ? for the unreadable current identity" "now:         ?/?"

# --- no builder.json at all ---
# A re-review would fail on the same missing record, so the remedy is
# tandem-pair, and the refusal must not claim the builder changed.
: > "$MARKER"
rm -f "$TMP/repo/.tandem-relay/builder.json"
RC=0
OUT=$(PATH="$TMP/bin:$PATH" HERDR_ENV=1 MARKER="$MARKER" REPO="$TMP/repo" BADID="$BADID" \
        "$HANDOFF" "$PLAN" 2>&1) || RC=$?

case "$RC" in
  24) ok "a missing builder.json exits 24" ;;
  *)  nope "expected rc 24 with no builder.json, got $RC" ;;
esac
chk "it says no identity is recorded"  "No builder identity is recorded at all"
chk "it names tandem-pair as the remedy" "Run tandem-pair"
# With no record, there is no builder to have changed.
nchk "it does not claim the builder changed" "is not the one that reviewed this plan"
# A re-review would fail at tandem-verdict on the same missing record.
if grep -q 'tandem-review' "$MARKER"; then
  nope "it requested a review that cannot complete without a builder record"
else
  ok "it requested no review it cannot complete"
fi
if grep -q 'agent prompt' "$MARKER"; then
  nope "the builder WAS prompted despite the refusal"
else
  ok "the builder was never prompted"
fi

# --- EXTRA's trailing newline survives defanging ---
# Command substitution strips trailing newlines, which would collapse the
# blank line after prompt_extra.
project 'true' 'REPO-EXTRA-MARKER'

cat > "$TMP/bin/tandem" <<'STUB'
#!/usr/bin/env bash
. "$(dirname "$0")/stub-common"
printf '%s\n' "$*" >> "$MARKER"
case "$1 $2" in
  "agent snapshot") printf '%%1\t%s\tidle\n' "$REPO"; exit 0 ;;
  "gate review")    printf '20260910-000001-1-1\n'; exit 0 ;;
esac
case "$1" in
  config) printf '{"kind":{"value":"codex"},"autonomy":{"value":"auto"}}\n'; exit 0 ;;
  paths)  printf '%s/.tandem-relay\n' "$REPO"; exit 0 ;;
esac
exit 0
STUB
chmod +x "$TMP/bin/tandem"

RC=0
OUT=$(PATH="$TMP/bin:$PATH" HERDR_ENV=1 MARKER="$MARKER" REPO="$TMP/repo" \
        "$HANDOFF" "$PLAN" --dry-run 2>&1) || RC=$?

# A literal newline in a case pattern tells one newline from a blank line.
case "$OUT" in
  *'REPO-EXTRA-MARKER'$'\n\n''When you finish'*)
    ok "prompt_extra's trailing newline survives: the blank line before the next paragraph is kept" ;;
  *)
    nope "prompt_extra's trailing newline was stripped, collapsing the blank line before the next paragraph (regression B)" ;;
esac
chk "prompt_extra still renders" "REPO-EXTRA-MARKER"

# --- reviewer gates: no reviewer, a builder's review, a changed reviewer ---
# Each refusal names its own remedy and none prompts the builder. A reason that
# needs a fresh review requests one; with no reviewer there is nobody to ask.
RVID="20260910-000080-1-1"
printf 'review body\n' > "$TMP/repo/.tandem-relay/reviews/$RVID.md"
printf '{"plan":"%s","verdict":"clean","kind":"codex","autonomy":"auto","reviewed_by":"reviewer","reviewer_kind":"claude","reviewer_model":"opus","reviewer_model_measured":true,"override_reason":""}\n' \
  "$PLAN" > "$TMP/repo/.tandem-relay/reviews/$RVID.json"
cat > "$TMP/bin/tandem" <<'STUB'
#!/usr/bin/env bash
. "$(dirname "$0")/stub-common"
printf '%s\n' "$*" >> "$MARKER"
case "$1 $2" in
  "agent snapshot") printf '%%1\t%s\tidle\n' "$REPO"; exit 0 ;;
  # A reviewer refusal puts the two sides between the reason and the id.
  "gate review")    printf '%s\n' "$GATE_REASON" ${GATE_SIDES:+$GATE_SIDES} "$RVID"; exit 24 ;;
esac
case "$1" in
  paths) printf '%s/.tandem-relay\n' "$REPO"; exit 0 ;;
esac
exit 0
STUB
# Logged again; an earlier section replaced this stub.
cat > "$TMP/bin/tandem-review" <<'STUB'
#!/usr/bin/env bash
printf 'tandem-review %s\n' "$*" >> "$MARKER"
exit 0
STUB
chmod +x "$TMP/bin/tandem" "$TMP/bin/tandem-review"
gate_handoff() {  # gate_handoff REASON ["WAS NOW"] -- hand off while the gate refuses with REASON
  : > "$MARKER"; RC=0
  OUT=$(PATH="$TMP/bin:$PATH" HERDR_ENV=1 MARKER="$MARKER" REPO="$TMP/repo" RVID="$RVID" GATE_REASON="$1" GATE_SIDES="${2:-}" \
          "$HANDOFF" "$PLAN" 2>&1) || RC=$?
}
never_prompted() { grep -q 'agent prompt' "$MARKER" && nope "$1: the builder WAS prompted despite the refusal" || ok "$1: the builder was never prompted"; }

rm -f "$TMP/repo/.tandem-relay/reviewer.json"
gate_handoff no_reviewer
[ "$RC" = 24 ] && ok "no reviewer exits 24" || nope "no reviewer exits 24 (got $RC)"
chk "no reviewer says the pair has none" "This pair has no reviewer"
chk "no reviewer names the block remedy" 'Declare a "reviewer" block in .tandem.json and re-run tandem-pair'
chk "no reviewer names the flag remedy" "tandem-pair --reviewer KIND"
chk "no reviewer says the plan was not delivered" "THE PLAN WAS NOT DELIVERED"
grep -q 'tandem-review' "$MARKER" && nope "no reviewer requested a review nobody can write" || ok "no reviewer requests no review"
never_prompted "no reviewer"

# The same refusal under --dry-run: 24 with the remedies and no review call.
: > "$MARKER"; RC=0
OUT=$(PATH="$TMP/bin:$PATH" HERDR_ENV=1 MARKER="$MARKER" REPO="$TMP/repo" RVID="$RVID" GATE_REASON=no_reviewer \
        "$HANDOFF" "$PLAN" --dry-run 2>&1) || RC=$?
[ "$RC" = 24 ] && ok "no reviewer exits 24 under --dry-run" || nope "no reviewer exits 24 under --dry-run (got $RC)"
chk "dry-run no reviewer names the block remedy" 'Declare a "reviewer" block in .tandem.json and re-run tandem-pair'
chk "dry-run no reviewer names the flag remedy" "tandem-pair --reviewer KIND"
nchk "dry-run no reviewer does not offer a review it cannot get" "would run: tandem-review"

write_reviewer "$TMP/repo/.tandem-relay" claude reviewer opus
gate_handoff builder_review
[ "$RC" = 24 ] && ok "a review written by the builder exits 24" || nope "a review written by the builder exits 24 (got $RC)"
chk "it says the builder wrote the review" "written by the builder"
chk "it says the builder never reviews" "never reviews"
grep -q "tandem-review $PLAN" "$MARKER" && ok "it asks the reviewer for a fresh review" || nope "it asks the reviewer for a fresh review"
never_prompted "a builder-written review"

write_reviewer "$TMP/repo/.tandem-relay" claude reviewer sonnet
gate_handoff reviewer "claude/opus claude/sonnet"
[ "$RC" = 24 ] && ok "a changed reviewer exits 24" || nope "a changed reviewer exits 24 (got $RC)"
chk "it says the reviewer is not the one that reviewed" "The reviewer is not the one that reviewed this plan"
chk "it names the old reviewer" "reviewed by: claude/opus"
chk "it names the new reviewer" "now:         claude/sonnet"
grep -q "tandem-review $PLAN" "$MARKER" && ok "it asks the reviewer for a fresh review after a change" || nope "it asks the reviewer for a fresh review after a change"
never_prompted "a changed reviewer"

write_reviewer "$TMP/repo/.tandem-relay" claude
gate_handoff reviewer "claude/opus claude/unmeasured"
chk "an unmeasured model now says unmeasured" "now:         claude/unmeasured"

printf '\nhandoff-review-gate: passed %d, failed %d\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

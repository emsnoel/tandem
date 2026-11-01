#!/usr/bin/env bash
# tandem-verdict hands a plan review back to the planner. This file checks its
# arguments and who may give a verdict; test-verdict-delivery.sh checks what
# it stores and sends.
set -uo pipefail
. "$(cd "$(dirname "$0")" && pwd)/lib/stub-verdict.sh"

# --- --help --------------------------------------------------------------------
run "$VERDICT_BIN" --help
[[ "$RC" == 0 ]] && ok "--help exits 0" || nope "--help exits 0 (got $RC)"
chk "--help prints usage" "usage: tandem-verdict"

# --- argument validation --------------------------------------------------------
run "$VERDICT_BIN" --review "$REVIEW" --verdict clean
[[ "$RC" == 1 ]] && ok "missing --plan exits 1" || nope "missing --plan exits 1 (got $RC)"
chk "missing --plan names the problem" "no --plan given"

run "$VERDICT_BIN" --plan "$PLAN" --verdict clean
[[ "$RC" == 1 ]] && ok "missing --review exits 1" || nope "missing --review exits 1 (got $RC)"
chk "missing --review names the problem" "no readable --review file given"

run "$VERDICT_BIN" --plan "$PLAN" --review "$TMP/missing.md" --verdict clean
[[ "$RC" == 1 ]] && ok "unreadable --review exits 1" || nope "unreadable --review exits 1 (got $RC)"

run "$VERDICT_BIN" --plan "$PLAN" --review "$REVIEW" --verdict maybe
[[ "$RC" == 1 ]] && ok "invalid --verdict exits 1" || nope "invalid --verdict exits 1 (got $RC)"
chk "invalid --verdict names the allowed values" "clean' or 'objections'"

run "$VERDICT_BIN" --plan "$TMP/no-such-plan.md" --review "$REVIEW" --verdict clean
[[ "$RC" == 1 ]] && ok "missing plan file exits 1" || nope "missing plan file exits 1 (got $RC)"
chk "missing plan file names the path" "plan not found"

# --- missing prerequisites ---
# The trimmed PATH keeps bash and jq and leaves out tandem.
TRIMMED="$TMP/trimmed-bin"; mkdir -p "$TRIMMED"
ln -s "$TMP/bin/jq" "$TRIMMED/jq"
ln -s "$(command -v bash)" "$TRIMMED/bash"
: > "$MARKER"
OUT=$(PATH="$TRIMMED" MAILBOX="$TMP/mailbox" REPO="$TMP/repo" MARKER="$MARKER" \
        "$VERDICT_BIN" --plan "$PLAN" --review "$REVIEW" --verdict clean 2>&1); RC=$?
[[ "$RC" == 1 ]] && ok "missing tandem on PATH exits 1" || nope "missing tandem on PATH exits 1 (got $RC)"
chk "missing tandem on PATH names it" "tandem not found on PATH"

# --- missing/incomplete builder identity ----------------------------------------
rm -f "$TMP/mailbox/builder.json"
run "$VERDICT_BIN" --plan "$PLAN" --review "$REVIEW" --verdict clean
[[ "$RC" == 1 ]] && ok "no builder.json exits 1" || nope "no builder.json exits 1 (got $RC)"
chk "no builder.json names the remedy" "Run tandem-pair"

printf '{"kind":"codex"}\n' > "$TMP/mailbox/builder.json"   # autonomy missing
run "$VERDICT_BIN" --plan "$PLAN" --review "$REVIEW" --verdict clean
[[ "$RC" == 1 ]] && ok "incomplete builder.json exits 1" || nope "incomplete builder.json exits 1 (got $RC)"
chk "incomplete builder.json names the remedy" "incomplete; re-run tandem-pair"

# From here on, a complete builder identity is present for every case.
write_builder "$TMP/mailbox"

# --- missing/incomplete reviewer record ------------------------------------------
rm -f "$TMP/mailbox/reviewer.json"
run "$VERDICT_BIN" --plan "$PLAN" --review "$REVIEW" --verdict clean
[[ "$RC" == 1 ]] && ok "no reviewer.json exits 1" || nope "no reviewer.json exits 1 (got $RC)"
chk "no reviewer.json names the reviewer block remedy" "Declare a \"reviewer\" block in .tandem.json and re-run tandem-pair"
chk "no reviewer.json names the flag remedy" "tandem-pair --reviewer KIND"
[[ -z "$(ls "$TMP/mailbox/reviews" 2>/dev/null)" ]] && ok "no reviewer.json writes no record" || nope "no reviewer.json writes no record"

printf '{"name":"reviewer"}\n' > "$TMP/mailbox/reviewer.json"   # kind missing
run "$VERDICT_BIN" --plan "$PLAN" --review "$REVIEW" --verdict clean
[[ "$RC" == 1 ]] && ok "incomplete reviewer.json exits 1" || nope "incomplete reviewer.json exits 1 (got $RC)"
chk "incomplete reviewer.json names the remedy" "is incomplete. Declare a \"reviewer\" block in .tandem.json and re-run tandem-pair"

# From here on, a complete reviewer is present too, measured on opus.
write_reviewer "$TMP/mailbox" claude reviewer opus

# --- only the reviewer gives a verdict -------------------------------------------
# The builder never reviews. A verdict from any other role, or from a shell
# with no role at all, is refused before anything is written or sent.
rm -rf "$TMP/mailbox/reviews" "$TMP/mailbox/q"
run "$VERDICT_BIN" --plan "$PLAN" --review "$REVIEW" --verdict clean --role builder
[[ "$RC" != 0 ]] && ok "--role builder is refused" || nope "--role builder is refused (rc $RC)"
chk "--role builder says a verdict comes from the reviewer pane only" "a verdict comes from the reviewer pane only"
[[ -z "$(ls "$TMP/mailbox/reviews" "$TMP/mailbox/q" 2>/dev/null)" ]] && ok "--role builder writes no record and queues nothing" || nope "--role builder writes no record and queues nothing"
run env HERDR_ROLE=builder "$VERDICT_BIN" --plan "$PLAN" --review "$REVIEW" --verdict clean
[[ "$RC" != 0 ]] && ok "HERDR_ROLE=builder alone is refused" || nope "HERDR_ROLE=builder alone is refused (rc $RC)"
run env -u HERDR_ROLE "$VERDICT_BIN" --plan "$PLAN" --review "$REVIEW" --verdict clean
[[ "$RC" != 0 ]] && ok "no role at all is refused, not defaulted to the builder" || nope "no role at all is refused (rc $RC)"
chk "no role at all says a verdict comes from the reviewer pane only" "a verdict comes from the reviewer pane only"

# --- in a Herdr pane, Herdr's name for the pane decides ---
nothing() { [[ -z "$(ls "$TMP/mailbox/reviews" "$TMP/mailbox/q" 2>/dev/null)" ]]; }
rm -rf "$TMP/mailbox/reviews" "$TMP/mailbox/q"
run env HERDR_PANE_ID=w1:p1 "$VERDICT_BIN" --plan "$PLAN" --review "$REVIEW" --verdict clean --role reviewer
[[ "$RC" != 0 ]] && nothing && ok "the builder pane claiming --role reviewer is refused, nothing recorded" \
  || nope "the builder pane claiming --role reviewer is refused (rc $RC): $OUT"
chk "the refusal names the pane Herdr says this is" "and this is builder"
rm -rf "$TMP/mailbox/reviews" "$TMP/mailbox/q"
run env -u HERDR_ROLE HERDR_PANE_ID=w1:p2 "$VERDICT_BIN" --plan "$PLAN" --review "$REVIEW" --verdict clean
[[ "$RC" == 0 && -n "$(ls "$TMP/mailbox/reviews"/*.json 2>/dev/null)" ]] && ok "the reviewer pane is accepted with no role" \
  || nope "the reviewer pane is accepted with no role (rc $RC): $OUT"
for f in timeout error; do
  rm -rf "$TMP/mailbox/reviews" "$TMP/mailbox/q"
  run env HERDR_PANE_ID=w1:p2 HERDR_FAKE=$f "$VERDICT_BIN" --plan "$PLAN" --review "$REVIEW" --verdict clean --role reviewer
  [[ "$RC" != 0 ]] && nothing && ok "a Herdr lookup that fails ($f) is refused, nothing recorded" \
    || nope "a Herdr lookup that fails ($f) is refused (rc $RC): $OUT"
  chk "the $f refusal says to check Herdr and retry" "Check that Herdr is running, then try again"
done
# A sandboxed Codex reviewer can see a pane id from its shared daemon while
# its sandbox denies the socket.
rm -rf "$TMP/mailbox/reviews" "$TMP/mailbox/q"
run env HERDR_PANE_ID=w1:p1 HERDR_FAKE=denied "$VERDICT_BIN" --plan "$PLAN" --review "$REVIEW" --verdict clean --role reviewer
[[ "$RC" == 0 && -n "$(ls "$TMP/mailbox/reviews"/*.json 2>/dev/null)" ]] && ok "an unreachable socket falls back to --role reviewer" \
  || nope "an unreachable socket falls back to --role reviewer (rc $RC): $OUT"
chk "and warns that Herdr cannot be reached" "warning: Herdr cannot be reached from this shell"
rm -rf "$TMP/mailbox/reviews" "$TMP/mailbox/q"
run env HERDR_PANE_ID=w1:p1 HERDR_FAKE=denied "$VERDICT_BIN" --plan "$PLAN" --review "$REVIEW" --verdict clean --role builder
[[ "$RC" != 0 ]] && nothing && ok "an unreachable socket with --role builder is refused" \
  || nope "an unreachable socket with --role builder is refused (rc $RC): $OUT"
# A custom reviewer name is what the pane and the role must match.
write_reviewer "$TMP/mailbox" claude critic opus
rm -rf "$TMP/mailbox/reviews" "$TMP/mailbox/q"
run "$VERDICT_BIN" --plan "$PLAN" --review "$REVIEW" --verdict clean --role reviewer
[[ "$RC" != 0 ]] && nothing && ok "--role reviewer is refused when the reviewer is named critic" \
  || nope "--role reviewer is refused when the reviewer is named critic (rc $RC): $OUT"
rm -rf "$TMP/mailbox/reviews" "$TMP/mailbox/q"
run "$VERDICT_BIN" --plan "$PLAN" --review "$REVIEW" --verdict clean --role critic
[[ "$RC" == 0 ]] && ok "--role critic is accepted for the reviewer named critic" || nope "--role critic is accepted (rc $RC): $OUT"
write_reviewer "$TMP/mailbox" claude reviewer opus


printf '\ntandem-verdict: passed %d, failed %d\n' "$PASS" "$FAIL"
(( FAIL == 0 ))

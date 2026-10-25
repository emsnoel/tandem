#!/usr/bin/env bash
# tandem-verdict hands a plan review back to the planner.
#
# Hermetic: tandem, tandem-project and herdr are stubbed and jq is symlinked.
# `tandem agent self --expect` is the real command, run against the stubbed
# herdr, because it decides whether a shell is the reviewer pane.
set -uo pipefail
. "$(cd "$(dirname "$0")" && pwd)/lib/records.sh"
VERDICT_BIN="${1:-$(cd "$(dirname "$0")/.." && pwd)/bin/tandem-verdict}"
[ -x "$VERDICT_BIN" ] || { echo "not executable: $VERDICT_BIN" >&2; exit 2; }

PASS=0; FAIL=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }
chk()  { case "$OUT" in *"$2"*) ok "$1" ;; *) nope "$1 (missing: $2)" ;; esac; }

command -v jq >/dev/null || { echo "need jq" >&2; exit 2; }

TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
mkdir -p "$TMP/bin" "$TMP/repo" "$TMP/mailbox"
# Symlink, not copy: macOS kills a copied /usr/bin/jq on exec.
ln -s "$(command -v jq)" "$TMP/bin/jq"
(cd "$(dirname "$0")/.." && go build -o "$TMP/tandem-real" ./cmd/tandem) || { echo "could not build tandem" >&2; exit 2; }
export TANDEM_REAL="$TMP/tandem-real"
# Cases about a pane set HERDR_PANE_ID themselves.
unset HERDR_PANE_ID

PLAN="$TMP/repo/p.md"; printf '# plan\n' > "$PLAN"
REVIEW="$TMP/review.md"; printf 'looks clean\n' > "$REVIEW"

# The stub logs every tandem call and answers the reads tandem-verdict makes.
# AGENT_SNAPSHOT_RC, AGENT_PROMPT_RC and AGENT_STATUS choose the branch;
# defaults are found, delivered, idle-then-working.
cat > "$TMP/bin/tandem" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$MARKER"
case "$1 $2" in
  "agent self"|"reviewer show")
    exec "$TANDEM_REAL" "$@" ;;
  "agent snapshot")
    [[ "${AGENT_SNAPSHOT_RC:-0}" == 0 ]] || exit "${AGENT_SNAPSHOT_RC}"
    printf 'w7:p1\t%s\t%s\n' "$REPO" "${SNAPSHOT_STATUS:-idle}"
    exit 0 ;;
  "agent status")
    printf '%s\n' "${AGENT_STATUS_SEQ:-working}"
    exit 0 ;;
  "agent prompt")
    if [[ "${AGENT_PROMPT_RC:-0}" != 0 ]]; then
      printf 'send-failed\tsimulated prompt failure\n' >&2
      exit "${AGENT_PROMPT_RC}"
    fi
    case "${CONFIRM_RC:-0}" in
      25) printf 'agent_never_started\tno turn began\n' >&2; exit 25 ;;
      26) printf 'agent_turn_failed\tturn ended at once\n%s\n' "${PANE_TEXT:-}" >&2; exit 26 ;;
    esac
    exit 0 ;;
esac
case "$1" in
  paths) printf '%s\n' "$MAILBOX"; exit 0 ;;
esac
exit 0
STUB
# tandem-project echoes --root back, as the real one does.
# HERDR_FAKE makes herdr hang (timeout), fail as the Codex sandbox does
# (denied), or return an error (error). Otherwise w1:p1 is the builder and
# w1:p2 the reviewer.
cat > "$TMP/bin/herdr" <<'STUB'
#!/usr/bin/env bash
case "${HERDR_FAKE:-}" in
  timeout) sleep 30; exit 0 ;;
  denied)  echo 'Error: Os { code: 1, kind: PermissionDenied, message: "Operation not permitted" }' >&2; exit 1 ;;
  error)   echo '{"error":{"code":"internal","message":"herdr failed"}}' >&2; exit 1 ;;
esac
[[ "$1 $2" == "agent list" ]] && { printf '{"result":{"agents":[{"pane_id":"w1:p1","name":"builder","agent_status":"idle"},{"pane_id":"w1:p2","name":"reviewer","agent_status":"idle"}]}}\n'; exit 0; }
exit 0
STUB
cat > "$TMP/bin/tandem-project" <<'STUB'
#!/usr/bin/env bash
printf '{"root":"%s"}\n' "$PWD"
STUB
chmod +x "$TMP/bin"/*

MARKER="$TMP/calls"
# Cases run as the reviewer pane unless they set HERDR_ROLE themselves.
export HERDR_ROLE=reviewer
run() { : > "$MARKER"; OUT=$(PATH="$TMP/bin:$PATH" MAILBOX="$TMP/mailbox" REPO="$TMP/repo" MARKER="$MARKER" "$@" 2>&1); RC=$?; }

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

# --- happy path: files are written atomically with the right content -----------
rm -rf "$TMP/mailbox/reviews"
run "$VERDICT_BIN" --plan "$PLAN" --review "$REVIEW" --verdict clean
[[ "$RC" == 0 ]] && ok "happy path exits 0" || nope "happy path exits 0 (got $RC)"
ID=$(ls "$TMP/mailbox/reviews" | grep '\.json$' | sed 's/\.json$//')
[[ -n "$ID" ]] && ok "a review id was created" || nope "a review id was created"
[[ -f "$TMP/mailbox/reviews/$ID.md" ]] && ok "the review body was written" || nope "the review body was written"
[[ -f "$TMP/mailbox/reviews/$ID.plan.md" ]] && ok "the plan copy was written" || nope "the plan copy was written"
if [[ "$(cat "$TMP/mailbox/reviews/$ID.md" 2>/dev/null)" == "looks clean" ]]; then
  ok "the review body matches the input file"
else
  nope "the review body matches the input file"
fi
if compgen -G "$TMP/mailbox/reviews/*.tmp" > /dev/null; then
  nope "no .tmp files were left behind (atomic rename)"
else
  ok "no .tmp files were left behind (atomic rename)"
fi
JSON=$(cat "$TMP/mailbox/reviews/$ID.json" 2>/dev/null)
[[ "$(jq -r '.verdict' <<<"$JSON")" == "clean" ]] && ok "the json records the verdict" || nope "the json records the verdict"
[[ "$(jq -r '.kind' <<<"$JSON")" == "codex" ]] && ok "the json records the builder kind" || nope "the json records the builder kind"
[[ "$(jq -r '.autonomy' <<<"$JSON")" == "auto" ]] && ok "the json records the builder autonomy" || nope "the json records the builder autonomy"
[[ "$(jq -r '.override_reason' <<<"$JSON")" == "" ]] && ok "the json starts with an empty override_reason" || nope "the json starts with an empty override_reason"
[[ "$(jq -r '.reviewed_by' <<<"$JSON")" == "reviewer" ]] && ok "the json records reviewed_by reviewer" || nope "the json records reviewed_by reviewer"
[[ "$(jq -r '.reviewer_kind' <<<"$JSON")" == "claude" ]] && ok "the json records the reviewer kind" || nope "the json records the reviewer kind"
[[ "$(jq -r '.reviewer_model' <<<"$JSON")" == "opus" ]] && ok "the json records the reviewer model" || nope "the json records the reviewer model"
[[ "$(jq -r '.reviewer_model_measured' <<<"$JSON")" == "true" ]] && ok "the json records that the reviewer model was measured" || nope "the json records that the reviewer model was measured"
chk "it reports the id and verdict" "review stored as $ID (clean)"

# --- planner not found: review is still stored, warns rather than dying --------
rm -rf "$TMP/mailbox/reviews"
: > "$MARKER"
OUT=$(PATH="$TMP/bin:$PATH" MAILBOX="$TMP/mailbox" REPO="$TMP/repo" MARKER="$MARKER" AGENT_SNAPSHOT_RC=10 \
        "$VERDICT_BIN" --plan "$PLAN" --review "$REVIEW" --verdict clean 2>&1); RC=$?
[[ "$RC" == 7 ]] && ok "planner not found exits 7" || nope "planner not found exits 7 (got $RC)"
chk "planner not found warns rather than dies silently" "the planner could not be found"
chk "planner not found says do not resend" "Do NOT resend the review"
if compgen -G "$TMP/mailbox/reviews/*.json" > /dev/null; then
  ok "the review is still stored when the planner is not found"
else
  nope "the review is still stored when the planner is not found"
fi

# --- socket unreachable: queued for the bridge ---
# Exit 3 is the normal case for a sandboxed agent, not a missing planner.
rm -rf "$TMP/mailbox/reviews" "$TMP/mailbox/q"
: > "$MARKER"
OUT=$(PATH="$TMP/bin:$PATH" MAILBOX="$TMP/mailbox" REPO="$TMP/repo" MARKER="$MARKER" AGENT_SNAPSHOT_RC=3 \
        "$VERDICT_BIN" --plan "$PLAN" --review "$REVIEW" --verdict objections 2>&1); RC=$?
[[ "$RC" == 0 ]] && ok "an unreachable socket queues the verdict and exits 0" \
                 || nope "an unreachable socket queues the verdict and exits 0 (got $RC)"
chk "it says the bridge will deliver it" "the bridge will deliver it"
case "$OUT" in *"could not be found"*) nope "it must not report a live planner as gone" ;;
               *) ok "it does not report the planner as gone" ;; esac
Q=$(ls "$TMP/mailbox/q/"*.json 2>/dev/null | head -1)
if [[ -n "$Q" ]]; then
  ok "a relay request was queued"
  [[ "$(jq -r '.kind' "$Q")" == verdict ]] && ok "the request is kind=verdict" \
                                          || nope "the request is kind=verdict (got $(jq -r '.kind' "$Q"))"
  [[ "$(jq -r '.to' "$Q")" == planner ]] && ok "the request names the planner" || nope "the request names the planner"
  jq -e '.message | contains("returned: objections") and contains("/reviews/")' "$Q" >/dev/null \
    && ok "the request carries the composed message, verdict and review path" \
    || nope "the request carries the composed message, verdict and review path"
  jq -e '.message | contains("the reviewer reviewed")' "$Q" >/dev/null \
    && ok "the message names the reviewer" \
    || nope "the message names the reviewer (got: $(jq -r '.message' "$Q" | head -1))"
  QID=$(jq -r '.id' "$Q")
  [[ -f "$TMP/mailbox/reviews/$QID.json" ]] && ok "the request id is the stored review's id" \
                                             || nope "the request id is the stored review's id"
else
  nope "a relay request was queued"
fi
grep -q '^agent prompt' "$MARKER" && nope "it tried to prompt the planner directly" \
                                  || ok "it does not try to prompt the planner directly"
if compgen -G "$TMP/mailbox/q/*.tmp" > /dev/null; then
  nope "no .tmp request was left behind (atomic rename)"
else
  ok "no .tmp request was left behind (atomic rename)"
fi

# --- from a sandboxed reviewer pane ---
# A Codex reviewer queues like a Codex builder. The sidecar records the
# builder's identity, never the reviewer's. builder.json is claude and
# reviewer.json codex here, so a sidecar copied from the wrong record shows.
rm -rf "$TMP/mailbox/reviews" "$TMP/mailbox/q"
write_builder "$TMP/mailbox" claude
write_reviewer "$TMP/mailbox" codex
: > "$MARKER"
OUT=$(PATH="$TMP/bin:$PATH" MAILBOX="$TMP/mailbox" REPO="$TMP/repo" MARKER="$MARKER" AGENT_SNAPSHOT_RC=3 \
        HERDR_ROLE=reviewer "$VERDICT_BIN" --plan "$PLAN" --review "$REVIEW" --verdict clean 2>&1); RC=$?
[[ "$RC" == 0 ]] && ok "the reviewer's verdict is queued and exits 0" \
                 || nope "the reviewer's verdict is queued and exits 0 (got $RC: $OUT)"
Q=$(ls "$TMP/mailbox/q/"*.json 2>/dev/null | head -1)
if [[ -n "$Q" ]]; then
  jq -e '.message | contains("the reviewer reviewed")' "$Q" >/dev/null \
    && ok "the message says the reviewer reviewed" \
    || nope "the message says the reviewer reviewed (got: $(jq -r '.message' "$Q" | head -1))"
  [[ "$(jq -r '.from' "$Q")" == reviewer ]] && ok "the request comes from the reviewer" \
                                          || nope "the request comes from the reviewer (got $(jq -r '.from' "$Q"))"
  SIDE="$TMP/mailbox/reviews/$(jq -r '.id' "$Q").json"
  [[ "$(jq -r '"\(.kind) \(.autonomy)"' "$SIDE" 2>/dev/null)" == "claude auto" ]] \
    && ok "the sidecar records the builder's identity, not the reviewer's" \
    || nope "the sidecar records the builder's identity (got $(jq -c . "$SIDE" 2>/dev/null))"
  [[ "$(jq -r '"\(.reviewer_kind) \(.reviewer_model_measured)"' "$SIDE" 2>/dev/null)" == "codex false" ]] \
    && ok "the sidecar records the reviewer and that its model was not measured" \
    || nope "the sidecar records the reviewer (got $(jq -c . "$SIDE" 2>/dev/null))"
else
  nope "the reviewer's verdict was queued"
fi

# A Codex pane runs commands without the pane's environment, so tandem-review
# puts --role into the command it asks for.
rm -rf "$TMP/mailbox/reviews" "$TMP/mailbox/q"
: > "$MARKER"
OUT=$(env -u HERDR_ROLE PATH="$TMP/bin:$PATH" MAILBOX="$TMP/mailbox" REPO="$TMP/repo" MARKER="$MARKER" AGENT_SNAPSHOT_RC=3 \
        "$VERDICT_BIN" --plan "$PLAN" --review "$REVIEW" --verdict clean --role reviewer 2>&1); RC=$?
Q=$(ls "$TMP/mailbox/q/"*.json 2>/dev/null | head -1)
[[ "$RC" == 0 && -n "$Q" ]] && jq -e '.message | contains("the reviewer reviewed")' "$Q" >/dev/null \
  && [[ "$(jq -r '.from' "$Q")" == reviewer ]] \
  && ok "--role names the reviewer with no HERDR_ROLE" \
  || nope "--role names the reviewer with no HERDR_ROLE (rc $RC: $(jq -c '{from, m: (.message | split("\n")[0])}' "$Q" 2>/dev/null) $OUT)"
rm -rf "$TMP/mailbox/reviews" "$TMP/mailbox/q"
OUT=$(PATH="$TMP/bin:$PATH" MAILBOX="$TMP/mailbox" REPO="$TMP/repo" MARKER="$MARKER" AGENT_SNAPSHOT_RC=3 \
        HERDR_ROLE=builder "$VERDICT_BIN" --plan "$PLAN" --review "$REVIEW" --verdict clean --role reviewer 2>&1); RC=$?
Q=$(ls "$TMP/mailbox/q/"*.json 2>/dev/null | head -1)
[[ -n "$Q" && "$(jq -r '.from' "$Q")" == reviewer ]] && ok "--role wins over HERDR_ROLE" \
  || nope "--role wins over HERDR_ROLE (got $(jq -r '.from' "$Q" 2>/dev/null))"
for bad in "" "re viewer" 'x$(id)'; do
  OUT=$(PATH="$TMP/bin:$PATH" MAILBOX="$TMP/mailbox" REPO="$TMP/repo" MARKER="$MARKER" AGENT_SNAPSHOT_RC=3 \
          "$VERDICT_BIN" --plan "$PLAN" --review "$REVIEW" --verdict clean --role "$bad" 2>&1); RC=$?
  [[ "$RC" != 0 ]] && ok "--role '$bad' is refused" || nope "--role '$bad' is refused (rc $RC)"
done
write_reviewer "$TMP/mailbox" claude reviewer opus
write_builder "$TMP/mailbox"

# --- prompt delivery fails: review is still stored, warns ----------------------
rm -rf "$TMP/mailbox/reviews"
: > "$MARKER"
OUT=$(PATH="$TMP/bin:$PATH" MAILBOX="$TMP/mailbox" REPO="$TMP/repo" MARKER="$MARKER" AGENT_PROMPT_RC=1 \
        "$VERDICT_BIN" --plan "$PLAN" --review "$REVIEW" --verdict clean 2>&1); RC=$?
[[ "$RC" == 7 ]] && ok "prompt delivery failure exits 7" || nope "prompt delivery failure exits 7 (got $RC)"
chk "prompt delivery failure names the reason" "simulated prompt failure"
if compgen -G "$TMP/mailbox/reviews/*.json" > /dev/null; then
  ok "the review is still stored when delivery fails"
else
  nope "the review is still stored when delivery fails"
fi

# --- planner idle, never wakes: exit 7 after the poll loop (about 8s) ---
rm -rf "$TMP/mailbox/reviews"
: > "$MARKER"
OUT=$(PATH="$TMP/bin:$PATH" MAILBOX="$TMP/mailbox" REPO="$TMP/repo" MARKER="$MARKER" \
        SNAPSHOT_STATUS=idle CONFIRM_RC=25 \
        "$VERDICT_BIN" --plan "$PLAN" --review "$REVIEW" --verdict clean 2>&1); RC=$?
[[ "$RC" == 7 ]] && ok "planner never wakes exits 7" || nope "planner never wakes exits 7 (got $RC)"
chk "planner never wakes warns" "never woke up"
if compgen -G "$TMP/mailbox/reviews/*.json" > /dev/null; then
  ok "the review is still stored when the planner never wakes"
else
  nope "the review is still stored when the planner never wakes"
fi

# --- planner idle, wakes on the first poll: success -----------------------------
rm -rf "$TMP/mailbox/reviews"
run env SNAPSHOT_STATUS=idle AGENT_STATUS_SEQ=working "$VERDICT_BIN" --plan "$PLAN" --review "$REVIEW" --verdict clean
[[ "$RC" == 0 ]] && ok "planner wakes on first poll: exits 0" || nope "planner wakes on first poll: exits 0 (got $RC)"
chk "planner wakes on first poll: notifies" "planner notified"

# --- planner already working before the prompt: no poll needed -----------------
rm -rf "$TMP/mailbox/reviews"
run env SNAPSHOT_STATUS=working "$VERDICT_BIN" --plan "$PLAN" --review "$REVIEW" --verdict clean
[[ "$RC" == 0 ]] && ok "planner already working: exits 0" || nope "planner already working: exits 0 (got $RC)"
chk "planner already working: notifies" "planner notified"

printf '\ntandem-verdict: passed %d, failed %d\n' "$PASS" "$FAIL"
(( FAIL == 0 ))

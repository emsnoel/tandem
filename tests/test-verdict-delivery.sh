#!/usr/bin/env bash
# What tandem-verdict stores and how it reaches the planner: the review files,
# the queue for a sandboxed reviewer, and each way delivery can fail.
set -uo pipefail
. "$(cd "$(dirname "$0")" && pwd)/lib/stub-verdict.sh"

# A complete builder and reviewer, as test-tandem-verdict.sh leaves them.
write_builder "$TMP/mailbox"
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


printf '\nverdict-delivery: passed %d, failed %d\n' "$PASS" "$FAIL"
(( FAIL == 0 ))

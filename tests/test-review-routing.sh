#!/usr/bin/env bash
# A plan review goes to the reviewer pane, and only there.
#
# The pair has a Claude builder and a Codex reviewer, so the reset typed shows
# which kind was used: /new is Codex's, /clear is Claude's.
#
# Runs the real bin/tandem-review and tandem binary with the fake Herdr.
set -uo pipefail
. "$(cd "$(dirname "$0")" && pwd)/lib/records.sh"
ROOT_DIR=$(cd "$(dirname "$0")/.." && pwd)

PASS=0; FAIL=0
ok()    { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope()  { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }
has()   { case "$2" in *"$3"*) ok "$1" ;; *) nope "$1 (missing: $3)" ;; esac; }
hasno() { case "$2" in *"$3"*) nope "$1 (present: $3)" ;; *) ok "$1" ;; esac; }

command -v jq >/dev/null || { echo "need jq" >&2; exit 2; }
TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
mkdir -p "$TMP/bin"
(cd "$ROOT_DIR" && go build -o "$TMP/bin/tandem" ./cmd/tandem) || { echo "could not build tandem"; exit 1; }
# A jq that fails: tandem-review asks tandem, so any jq call is a bug.
printf '#!/bin/sh\necho "jq was called: $*" >&2\nexit 99\n' > "$TMP/bin/jq"
chmod +x "$TMP/bin/jq"
. "$ROOT_DIR/tests/lib/fake-herdr.sh"
. "$ROOT_DIR/tests/lib/fake-socket.sh"
FAKE="$TMP/fake"
fake_herdr_install "$TMP/bin" "$FAKE"
FAKE="$FAKE" PATH="$TMP/bin:$PATH" fake_socket_start "$TMP" || exit 1

REPO="$TMP/repo"; MB="$REPO/.tandem-relay"
mkdir -p "$REPO/docs/plans" "$MB"; git -C "$REPO" init -q; REPO=$(cd "$REPO" && pwd -P); MB="$REPO/.tandem-relay"
printf '{"plans_dir":"docs/plans","check_command":"true"}\n' > "$REPO/.tandem.json"
PLAN="$REPO/docs/plans/p.md"
printf '# plan\n### Task 1: one\nstep\n' > "$PLAN"
write_builder "$MB" claude
REVIEWER_REC='{"kind":"codex","name":"reviewer"}'

# review [args...]: run tandem-review against a fresh fake pane; sets OUT, RC.
review() {
  rm -f "$FAKE/prompts" "$FAKE/resets" "$FAKE"/prompt.*.txt
  fake_set_state idle
  OUT=$(cd "$REPO" && env -u HERDR_DECISION -u HERDR_JUDGE PATH="$TMP/bin:$ROOT_DIR/bin:/usr/bin:/bin" \
        HERDR_ENV=1 FAKE="$FAKE" REPO="$REPO" tandem-review "$PLAN" "$@" 2>&1); RC=$?
}
sent_to()  { cut -f1 "$FAKE/prompts" 2>/dev/null | tr '\n' ' '; }
resets()   { cat "$FAKE/resets" 2>/dev/null | tr '\t\n' ': '; }

printf '== a reviewer is recorded ==\n'
printf '%s\n' "$REVIEWER_REC" > "$MB/reviewer.json"; rm -f "$FAKE/absent"
review --dry-run
[ "$RC" -eq 0 ] && ok "a dry run succeeds" || nope "a dry run succeeds (rc $RC: $OUT)"
has "the header names the reviewer pane" "$(printf '%s' "$OUT" | head -1)" "tandem-review: reviewer reviewer (pane"
[ ! -s "$FAKE/prompts" ] && [ ! -s "$FAKE/resets" ] && ok "a dry run types nothing" || nope "a dry run typed: $(sent_to) $(resets)"

review
[ "$RC" -eq 0 ] && ok "a review succeeds" || nope "a review succeeds (rc $RC: $OUT)"
[ "$(sent_to)" = "reviewer " ] && ok "the prompt goes to the reviewer, and only there" || nope "prompted: $(sent_to)"
[ "$(resets)" = "reviewer:/new " ] && ok "the reviewer is reset with Codex's /new, not the builder's /clear" || nope "resets: $(resets)"
has "delivery is confirmed against the reviewer" "$OUT" "review requested (reviewer now working)"

printf '\n== the prompt does not assume its reader builds ==\n'
P=$(cat "$FAKE/prompt.1.txt" 2>/dev/null)
hasno "no 'before you implement it'" "$P" "before you implement it"
has "says another agent implements" "$P" "Review the plan at $PLAN. Another agent will implement it."
has "still forbids starting task 1" "$P" "Do not start"
hasno "does not tell the reader it builds" "$P" "Those are yours to decide when you build"
has "names the reviewer in the verdict command it asks for" "$P" 'tandem-verdict --plan "'"$PLAN"'" --role reviewer'

printf '\n== class 4 asks about the builder'"'"'s authority, not the reviewer'"'"'s ==\n'
has "class 4 names the builder's authority" "$P" "a step needs authority the builder"
has "with the builder's kind and autonomy from builder.json" "$P" "(claude, autonomy auto, from builder.json) does not have"
has "says the reviewer's own sandbox proves nothing about the builder" "$P" "proves nothing about the builder"
has "and asks for 'could not verify', not an objection" "$P" 'report it as "could not verify", not as an objection'
hasno "no longer asks about authority the reader does not have" "$P" "authority you do not have"

printf '\n== no builder recorded: nothing is sent ==\n'
mv "$MB/builder.json" "$MB/builder.json.bak"
review
[ "$RC" -eq 1 ] && ok "exits 1" || nope "exits 1 (rc $RC: $OUT)"
has "says to run tandem-pair" "$OUT" "Run tandem-pair"
[ ! -s "$FAKE/prompts" ] && [ ! -s "$FAKE/resets" ] && ok "types nothing into any pane" || nope "typed: $(sent_to) $(resets)"
mv "$MB/builder.json.bak" "$MB/builder.json"

printf '\n== an incomplete builder record: nothing is sent ==\n'
cp "$MB/builder.json" "$MB/builder.json.bak"
for rec in '{"kind":"claude"}' 'not json'; do
  printf '%s\n' "$rec" > "$MB/builder.json"
  review
  [ "$RC" -eq 1 ] && ok "builder.json $rec: exits 1" || nope "builder.json $rec: exits 1 (rc $RC: $OUT)"
  has "builder.json $rec: says no complete builder identity is recorded" "$OUT" "no complete builder identity"
  [ ! -s "$FAKE/prompts" ] && [ ! -s "$FAKE/resets" ] && ok "builder.json $rec: types nothing" || nope "typed: $(sent_to) $(resets)"
done
mv "$MB/builder.json.bak" "$MB/builder.json"

printf '\n== a manifest that is not a JSON object is named ==\n'
mv "$REPO/.tandem.json" "$REPO/.tandem.json.bak"
printf '[1]\n' > "$REPO/.tandem.json"
review --dry-run
has "warns that the manifest was ignored" "$OUT" "tandem-review: WARNING $REPO/.tandem.json is not a JSON object, so its settings were ignored."
mv "$REPO/.tandem.json.bak" "$REPO/.tandem.json"

printf '\n== a missing prompt template: nothing is sent ==\n'
cp -R "$ROOT_DIR/bin" "$TMP/rbin"
rm "$TMP/rbin/lib/review-rules.txt"
rm -f "$FAKE/prompts" "$FAKE/resets"; fake_set_state idle
OUT=$(cd "$REPO" && PATH="$TMP/bin:$ROOT_DIR/bin:/usr/bin:/bin" HERDR_ENV=1 FAKE="$FAKE" REPO="$REPO" \
      "$TMP/rbin/tandem-review" "$PLAN" 2>&1); RC=$?
[ "$RC" -eq 1 ] && ok "exits 1" || nope "exits 1 (rc $RC: $OUT)"
has "names the missing template" "$OUT" "the prompt template $TMP/rbin/lib/review-rules.txt is missing or empty; re-run install.sh. NOTHING WAS SENT."
[ ! -s "$FAKE/prompts" ] && [ ! -s "$FAKE/resets" ] && ok "types nothing into any pane" || nope "typed: $(sent_to) $(resets)"

printf '\n== --builder is gone: the builder never reviews ==\n'
review --builder
[ "$RC" -eq 2 ] && ok "exits 2" || nope "exits 2 (rc $RC: $OUT)"
has "says the builder never reviews" "$OUT" "the builder never reviews"
[ ! -s "$FAKE/prompts" ] && [ ! -s "$FAKE/resets" ] && ok "types nothing into any pane" || nope "typed: $(sent_to) $(resets)"

printf '\n== the recorded reviewer has no pane ==\n'
printf 'reviewer\n' > "$FAKE/absent"
review
[ "$RC" -eq 1 ] && ok "exits 1" || nope "exits 1 (rc $RC)"
has "says to re-pair" "$OUT" "Re-run tandem-pair"
hasno "does not offer the builder as a way out" "$OUT" "--builder"
has "says nothing was sent" "$OUT" "NOTHING WAS SENT"
[ ! -s "$FAKE/prompts" ] && [ ! -s "$FAKE/resets" ] && ok "types nothing into any pane" || nope "typed: $(sent_to) $(resets)"
rm -f "$FAKE/absent"

printf '\n== reviewer.json cannot be read ==\n'
printf 'not json\n' > "$MB/reviewer.json"
review
[ "$RC" -eq 1 ] && ok "exits 1" || nope "exits 1 (rc $RC)"
has "names the file" "$OUT" "$MB/reviewer.json"
[ ! -s "$FAKE/prompts" ] && [ ! -s "$FAKE/resets" ] && ok "types nothing into any pane" || nope "typed: $(sent_to) $(resets)"

printf '\n== no reviewer recorded: nothing is sent, and the builder is not a fallback ==\n'
rm -f "$MB/reviewer.json"
for mode in "" --dry-run; do
  review $mode
  [ "$RC" -eq 1 ] && ok "${mode:-a review} exits 1" || nope "${mode:-a review} exits 1 (rc $RC: $OUT)"
  has "${mode:-a review} says the pair has no reviewer" "$OUT" "this pair has no reviewer"
  has "${mode:-a review} names the block remedy" "$OUT" "Declare a \"reviewer\" block in .tandem.json and re-run tandem-pair"
  has "${mode:-a review} names the flag remedy" "$OUT" "tandem-pair --reviewer KIND"
  [ ! -s "$FAKE/prompts" ] && [ ! -s "$FAKE/resets" ] && ok "${mode:-a review} types nothing into any pane" || nope "typed: $(sent_to) $(resets)"
done

printf '\n%s passed, %s failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

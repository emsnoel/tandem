#!/usr/bin/env bash
# A gate that fails open still looks like a gate. This proves it never does.
#
# Every input to `tandem gate review` can be unreadable, and "" == "" would
# read as unchanged.
#
# Hermetic: a real repo in a temp dir and the real binary; the gate makes no
# socket calls.
set -uo pipefail
TANDEM="${1:-$(command -v tandem)}"
[ -x "$TANDEM" ] || { echo "tandem not found; run ./install.sh" >&2; exit 2; }

PASS=0; FAIL=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }

TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
REPO="$TMP/repo"; mkdir -p "$REPO"
git -C "$REPO" init -q
git -C "$REPO" config user.email t@example.com
git -C "$REPO" config user.name test
mkdir -p "$REPO/docs/plans"
PLAN="$REPO/docs/plans/p.md"
printf '# plan\n' > "$PLAN"
git -C "$REPO" add -A && git -C "$REPO" commit -qm init

RV="$REPO/.tandem-relay/reviews"; mkdir -p "$RV"

# write_builder <kind> <autonomy>: the record bin/tandem-pair writes.
write_builder() {
  jq -n --arg k "$1" --arg a "$2" '{kind:$k, autonomy:$a, adopted:false}' \
    > "$REPO/.tandem-relay/builder.json"
}
write_builder codex auto
# write_reviewer <kind> <model> <measured>: the record bin/tandem-pair writes.
write_reviewer() {
  jq -n --arg k "$1" --arg m "$2" --argjson mm "$3" \
    '{kind:$k, name:"reviewer", model:$m, autonomy:"auto", model_measured:$mm}' \
    > "$REPO/.tandem-relay/reviewer.json"
}
write_reviewer claude opus true
# write_review <id> <verdict> [override_reason]: a complete, valid review. The
# plan copy is made with cp, as tandem-verdict does, since command
# substitution strips trailing newlines and the gate compares exactly.
write_review() {
  local id="$1" verdict="$2" o="${3:-}"
  cp "$PLAN" "$RV/$id.plan.md"
  printf 'review body\n'  > "$RV/$id.md"
  jq -n --arg plan "$PLAN" --arg v "$verdict" --arg o "$o" \
     '{plan:$plan, verdict:$v, kind:"codex", autonomy:"auto",
       reviewed_by:"reviewer", reviewer_kind:"claude", reviewer_model:"opus",
       reviewer_model_measured:true, override_reason:$o}' > "$RV/$id.json"
}

# run <extra-args...>: returns rc and sets OUT.
run() { OUT=$("$TANDEM" gate review --plan "$PLAN" --root "$REPO" "$@" 2>&1); return $?; }

expect() { # expect <label> <want-rc> <want-first-line>
  local label="$1" want="$2" detail="$3"
  case "$RC" in
    "$want") ;;
    *) nope "$label (rc=$RC, want $want)"; return ;;
  esac
  if [ -z "$detail" ]; then ok "$label"; return; fi
  case "$(printf '%s' "$OUT" | head -1)" in
    "$detail") ok "$label" ;;
    *) nope "$label (detail=$(printf '%s' "$OUT" | head -1), want $detail)" ;;
  esac
}

RC=0; run || RC=$?
expect "no review at all refuses" 24 absent

# No reviewer recorded outranks "no review": nobody could write one.
mv "$REPO/.tandem-relay/reviewer.json" "$REPO/.tandem-relay/reviewer.json.off"
RC=0; run || RC=$?
expect "no reviewer and no review refuses no_reviewer, not absent" 24 no_reviewer
mv "$REPO/.tandem-relay/reviewer.json.off" "$REPO/.tandem-relay/reviewer.json"

write_review 20260910-000001-1-1 clean
RC=0; run || RC=$?
expect "a clean current review allows" 0 ""
# A pass prints the review id with no reason line, which is how
# bin/tandem-handoff finds a stored ruling.
case "$OUT" in
  "20260910-000001-1-1") ok "a pass names the review it judged" ;;
  *) nope "a pass names the review it judged (got: $OUT)" ;;
esac

printf '# edited\n' > "$PLAN"
RC=0; run || RC=$?
expect "an edited plan refuses" 24 plan
printf '# plan\n' > "$PLAN"

write_builder claude auto
RC=0; run || RC=$?
expect "a changed builder kind refuses" 24 builder
write_builder codex auto

write_builder codex ask
RC=0; run || RC=$?
expect "a changed builder autonomy refuses" 24 builder
write_builder codex auto

# --- the reviewer binding ---
# A plan review is bound to the reviewer that wrote it. Each case breaks one
# thing and restores it.
rm -f "$REPO/.tandem-relay/reviewer.json"
RC=0; run || RC=$?
expect "no reviewer recorded refuses" 24 no_reviewer
write_reviewer claude opus true

# A review with no reviewed_by: nothing says who wrote it.
jq 'del(.reviewed_by, .reviewer_kind, .reviewer_model, .reviewer_model_measured)' \
  "$RV/20260910-000001-1-1.json" > "$RV/tmp.json" && mv "$RV/tmp.json" "$RV/20260910-000001-1-1.json"
RC=0; run || RC=$?
expect "a pre-upgrade review counts as the builder's and refuses" 24 builder_review
write_review 20260910-000001-1-1 clean

jq '.reviewed_by = "builder"' "$RV/20260910-000001-1-1.json" > "$RV/tmp.json" && mv "$RV/tmp.json" "$RV/20260910-000001-1-1.json"
RC=0; run || RC=$?
expect "a review the builder wrote refuses" 24 builder_review
write_review 20260910-000001-1-1 clean

write_reviewer codex opus true
RC=0; run || RC=$?
expect "a changed reviewer kind refuses" 24 reviewer
write_reviewer claude sonnet true
RC=0; run || RC=$?
expect "a changed reviewer model refuses" 24 reviewer
# Unmeasured now: the model cannot be compared, only the kind.
write_reviewer claude sonnet false
RC=0; run || RC=$?
expect "an unmeasured reviewer model with the same kind allows" 0 ""
write_reviewer codex sonnet false
RC=0; run || RC=$?
expect "an unmeasured reviewer model with another kind refuses" 24 reviewer
write_reviewer claude opus true

# No builder.json and an empty identity in the sidecar: "" equals "", so only
# the IdentityMeasured guard can refuse. The verdict is clean so that removing
# the guard would make this pass and fail the test. The record is written by
# hand because write_review sets a kind and autonomy.
rm -f "$REPO/.tandem-relay/builder.json"
cp "$PLAN" "$RV/20260910-000001-1-1.plan.md"
printf 'review body\n' > "$RV/20260910-000001-1-1.md"
jq -n --arg plan "$PLAN" '{plan:$plan, verdict:"clean", kind:"", autonomy:"",
  reviewed_by:"reviewer", reviewer_kind:"claude", reviewer_model:"opus",
  reviewer_model_measured:true, override_reason:""}' \
  > "$RV/20260910-000001-1-1.json"
RC=0; run || RC=$?
expect "an unresolvable identity refuses" 24 builder
write_builder codex auto
write_review 20260910-000001-1-1 clean

# Committing the plan must not void its review: only the plan text counts.
git -C "$REPO" add -A && git -C "$REPO" commit -qm "commit the plan"
RC=0; run || RC=$?
expect "committing the plan does not void its review" 0 ""

# An unreadable plan copy: both texts must read, or neither compares.
chmod 000 "$RV/20260910-000001-1-1.plan.md" 2>/dev/null
RC=0; run || RC=$?
if [ "$(id -u)" = "0" ]; then
  ok "an unreadable plan copy refuses (skipped: running as root)"
else
  expect "an unreadable plan copy refuses" 24 plan
fi
chmod 644 "$RV/20260910-000001-1-1.plan.md" 2>/dev/null

printf 'not json at all' > "$RV/20260910-000001-1-1.json"
RC=0; run || RC=$?
expect "a malformed sidecar refuses rather than verdicting clean" 24 absent
write_review 20260910-000001-1-1 clean

write_review 20260910-000002-1-1 objections
RC=0; run || RC=$?
expect "standing objections refuse" 24 objections

write_review 20260910-000002-1-1 objections "I commit here, not you"
RC=0; run || RC=$?
expect "objections overruled with a reason allow" 0 ""

write_review 20260910-000002-1-1 objections "   "
RC=0; run || RC=$?
expect "a blank override is not an override" 24 objections

write_review 20260910-000002-1-1 objections

# Every other review has objections, so only the newest (id-3, clean) gives
# rc 0.
write_review 20260910-000001-1-1 objections
write_review 20260910-000003-1-1 clean
RC=0; run || RC=$?
expect "the newest review wins" 0 ""

# An older clean review and a newer sidecar that will not parse. The newer one
# might object to this plan, so fail closed.
write_review 20260910-000004-1-1 clean
printf 'not json at all' > "$RV/20260910-000005-1-1.json"
RC=0; run || RC=$?
expect "a newer malformed sidecar refuses despite an older clean review" 24 absent
rm -f "$RV/20260910-000005-1-1.json"

write_review 20260910-000009-1-1 clean
RC=0; run --expect-review 20260910-000001-1-1 || RC=$?
expect "an override aimed at a superseded review refuses" 24 superseded
RC=0; run --expect-review 20260910-000009-1-1 || RC=$?
expect "an override aimed at the newest review is allowed" 0 ""

printf '\nreview-gate: passed %d, failed %d\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

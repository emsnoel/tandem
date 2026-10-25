#!/usr/bin/env bash
# Real manifest resolution and start validation. Herdr and launches are
# stubbed, so no prompt reaches an agent.
set -uo pipefail
. "$(cd "$(dirname "$0")" && pwd)/lib/records.sh"
ROOT_DIR=$(cd "$(dirname "$0")/.." && pwd)
TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
REPO="$TMP/repo with spaces"; RELAY="$REPO/.tandem-relay"
mkdir -p "$TMP/bin" "$REPO/docs/plans" "$RELAY"
git -C "$REPO" init -q
printf '# Plan\n\n**Goal:** g.\n\n### Task 1: One\n\nDo one.\n' > "$REPO/docs/plans/p.md"
write_builder "$RELAY"; write_reviewer "$RELAY"
(cd "$ROOT_DIR" && go build -o "$TMP/tandem-real" ./cmd/tandem) || exit 1
ln -s "$ROOT_DIR/bin/tandem-project" "$TMP/bin/tandem-project"
printf '#!/usr/bin/env bash\nexit 0\n' > "$TMP/bin/herdr"
cp "$TMP/bin/herdr" "$TMP/bin/tandem-review"
cat > "$TMP/bin/tandem" <<'STUB'
#!/usr/bin/env bash
case "$1 $2" in
  'paths manifest') printf '%s/.tandem.json\n' "$REPO"; exit 0 ;;
  'paths relay') printf '%s\n' "$RELAY"; exit 0 ;;
  'agent snapshot') printf '%%1\t%s\tidle\n' "$REPO"; exit 0 ;;
  'build start')
    printf '%s\n' "$@" | jq -Rn '[inputs]' > "$CALLS"
    [[ "${REAL_START:-0}" == 1 ]] && exec "$TANDEM_REAL" "$@"
    printf '{"dry_run":true,"engine":"durable"}\n'; exit 0 ;;
esac
exit 0
STUB
chmod +x "$TMP/bin/"*
PASS=0; FAIL=0
ok() { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }
run() {
  rm -f "$TMP/calls"
  OUT=$(env -u TANDEM_BUILD_ENGINE PATH="$TMP/bin:$PATH" HERDR_ENV=1 HERDR_JUDGE=0 REPO="$REPO" RELAY="$RELAY" CALLS="$TMP/calls" TANDEM_REAL="$TMP/tandem-real" REAL_START="${REAL_START:-0}" "$ROOT_DIR/bin/tandem-handoff" "$REPO/docs/plans/p.md" --dry-run "$@" 2>&1); RC=$?
}
args_have() { jq -e --arg f "$1" --arg v "$2" 'index($f) as $i | $i != null and .[$i+1] == $v' "$TMP/calls" >/dev/null; }
no_review_args() { ! jq -e 'any(.[]; . == "--review-delivery" or . == "--acceptance-policy" or . == "--collaboration")' "$TMP/calls" >/dev/null; }
manifest() { printf '%s\n' "$1" > "$REPO/.tandem.json"; }

manifest '{"build_engine":"durable","review_delivery":"mailbox","acceptance_policy":"policies/acceptance policy.json","collaboration":"collab-configured"}'
run
[[ "$RC" == 0 ]] && args_have --review-delivery mailbox && args_have --acceptance-policy "$REPO/policies/acceptance policy.json" && args_have --collaboration collab-configured \
  && ok 'durable handoff forwards manifest settings and resolves policy against root' || nope "configured arguments: $OUT $(cat "$TMP/calls")"
manifest "{\"build_engine\":\"durable\",\"acceptance_policy\":\"$TMP/absolute policy.json\"}"
run
args_have --acceptance-policy "$TMP/absolute policy.json" && ok 'absolute policy stays absolute' || nope 'absolute policy was changed'
manifest '{"build_engine":"durable"}'
run
no_review_args && ok 'unset configuration adds no defaults or invented identifiers' || nope 'unset configuration invented review arguments'
manifest '{"build_engine":"legacy","review_delivery":"mailbox","acceptance_policy":"missing.json","collaboration":"c"}'
run
no_review_args && ok 'legacy handoff receives none of the durable review flags' || nope 'legacy received durable review flags'
manifest '{"build_engine":"durable","review_delivery":"mailbox"}'
REAL_START=1 run
[[ "$RC" != 0 && "$OUT" == *'--collaboration'* && ! -f "$RELAY/active-run.json" ]] \
  && ok 'incomplete mailbox configuration fails real start validation before launch' || nope "missing collaboration accepted: rc=$RC $OUT"
manifest '{"build_engine":"durable","review_delivery":"mailbox","collaboration":"collab-configured"}'
REAL_START=1 run
[[ "$RC" != 0 && "$OUT" == *'--acceptance-policy'* && ! -f "$RELAY/active-run.json" ]] \
  && ok 'a collaboration without an acceptance policy also fails before launch' || nope "missing policy accepted: rc=$RC $OUT"
# A resumed run keeps its own protocol and policy, so new manifest settings
# must not block its recovery.
printf '{"run_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}\n' > "$RELAY/active-run.json"
run --continue
no_review_args && ok 'existing run continues with stored review configuration' || nope 'resume received new review settings'
printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"
[[ "$FAIL" == 0 ]]

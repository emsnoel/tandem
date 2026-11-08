#!/usr/bin/env bash
# A reviewer that waits for a subagent or a command must wait once, not poll.
#
# Each poll re-sends the builder's whole context to learn nothing. The prompt
# names no tool or number, because one template drives three CLIs with
# different wait options. Under Codex there are two yields, so it says "raise
# every one of them".
set -uo pipefail
. "$(cd "$(dirname "$0")" && pwd)/lib/records.sh"

PASS=0; FAIL=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }

TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
. "$(cd "$(dirname "$0")" && pwd)/lib/stub-handoff.sh"
stub_handoff_env "$TMP"
mkdir -p "$TMP/repo/docs/plans" "$TMP/repo/.tandem-relay"
# The builder never reviews: tandem-review renders the reviewer's prompt.
write_reviewer "$TMP/repo/.tandem-relay" codex
# Class 4 names the builder's kind and autonomy, from builder.json.
write_builder "$TMP/repo/.tandem-relay" claude
PLAN="$TMP/repo/docs/plans/p.md"; printf '# plan\n' > "$PLAN"
REVIEW="$(cd "$(dirname "$0")/.." && pwd)/bin/tandem-review"

# render SCRIPT CHECK: the prompt SCRIPT would send, without its diagnostics.
render() {
  PATH="$TMP/bin:$PATH" HERDR_ENV=1 REPO="$TMP/repo" STUB_CHECK="$2" \
    "$1" "$PLAN" --dry-run 2>/dev/null \
    | awk '/^--- prompt that would be sent ---$/{f=1;next} /^--- end ---$/{f=0} f'
}
flat() { printf '%s' "$1" | tr '\n' ' ' | tr -s ' '; }

RFLAT=$(flat "$(render "$REVIEW" "make check")")
case "$RFLAT" in
  *"in ONE blocking call (do not poll it)"*) ok "review prompt says one blocking call" ;;
  *) nope "review prompt says one blocking call" ;;
esac

printf '\n%s passed, %s failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

#!/usr/bin/env bash
# A builder that waits for a subagent or a command must wait once, not poll.
#
# Each poll re-sends the builder's whole context to learn nothing. The prompt
# names no tool or number, because one template drives four CLIs with
# different wait options. Under Codex there are two yields, so it says "raise
# every one of them".
set -uo pipefail
. "$(cd "$(dirname "$0")" && pwd)/lib/records.sh"
HANDOFF="${1:-$(cd "$(dirname "$0")/.." && pwd)/bin/tandem-handoff}"
[ -x "$HANDOFF" ] || { echo "not executable: $HANDOFF" >&2; exit 2; }

PASS=0; FAIL=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }
# The template is hard-wrapped, so match against a flattened copy.
chkp() { case "$FLAT" in *"$2"*) ok "$1" ;; *) nope "$1 (missing: $2)" ;; esac; }
chkno() { case "$FLAT" in *"$2"*) nope "$1 (present but must not be: $2)" ;; *) ok "$1" ;; esac; }

TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
. "$(cd "$(dirname "$0")" && pwd)/lib/stub-handoff.sh"
stub_handoff_env "$TMP"
# tandem-review still calls jq until roadmap item 5's PR 4.
ln -sf "$(command -v jq)" "$TMP/bin/jq"
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
# The waiting paragraph, from its heading to the next one.
waiting() { printf '%s\n' "$1" | awk '/^WAIT ONCE, NOT IN A LOOP\.$/{f=1} /^ASK BEFORE ASSUMING/{f=0} f'; }

PROMPT=$(render "$HANDOFF" "make check")
FLAT=$(flat "$PROMPT")

[ -n "$PROMPT" ] || { nope "the prompt rendered at all"; printf '\n%s passed, %s failed\n' "$PASS" "$FAIL"; exit 1; }

printf '== wait once, not in a loop ==\n'
chkp "tells the builder to wait in a single call" \
     "WAIT ONCE, NOT IN A LOOP."
chkp "says to use the longest timeout available"  \
     "longest timeout"
chkp "gives the reason: a reissued wait re-sends the context" \
     "re-sends your whole context"

# One template drives four builder kinds, so it names no CLI's tool or value.
printf '\n== stays builder-neutral ==\n'
chkno "names no builder-specific wait tool" "wait_agent"
chkno "names no hardcoded millisecond value" "3600000"
chkno "names no hardcoded timeout parameter" "timeout_ms"

printf '\n== long commands run in one call ==\n'
chkp "covers long commands, not only subagents"  "The same goes for long commands"
chkp "names the check the builder runs"          'the check (`tandem check`)'
chkp "says one blocking call"                     "Run each in ONE blocking call"
chkp "covers tools with more than one timeout"   "raise every one of them"
chkp "gives the reason: polling re-sends context" "re-sends your whole context each time"

NOCHECK=$(flat "$(render "$HANDOFF" "")")
case "$NOCHECK" in
  *"long commands - a full test run, a build."*) ok "reads cleanly with no check command" ;;
  *) nope "reads cleanly with no check command" ;;
esac
case "$NOCHECK" in
  *"the check ("*|*"    tandem check"*) nope "names a check command that does not exist" ;;
  *) ok "names no check command when there is none" ;;
esac

printf '\n== the waiting paragraph stays builder-neutral ==\n'
# Use a check command with no digits, so only tandem's own words are tested.
PARA=$(waiting "$PROMPT")
[ -n "$PARA" ] || nope "the waiting paragraph was found"
for word in exec_command write_stdin yield_time_ms wait_agent timeout_ms; do
  case "$PARA" in *"$word"*) nope "names no tool or parameter ($word)" ;; *) ok "names no tool or parameter ($word)" ;; esac
done
case "$PARA" in *[0-9]*) nope "contains no number" ;; *) ok "contains no number" ;; esac

# A check command with digits is still quoted verbatim.
DIGITS=$(flat "$(render "$HANDOFF" "npm run check -- --max-workers=2")")
case "$DIGITS" in
  *"the project's check (npm run check -- --max-workers=2)"*) ok "names a check command containing digits verbatim" ;;
  *) nope "names a check command containing digits verbatim" ;;
esac

printf '\n== the review prompt does not poll its check either ==\n'
RFLAT=$(flat "$(render "$REVIEW" "make check")")
case "$RFLAT" in
  *"in ONE blocking call (do not poll it)"*) ok "review prompt says one blocking call" ;;
  *) nope "review prompt says one blocking call" ;;
esac

printf '\n%s passed, %s failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

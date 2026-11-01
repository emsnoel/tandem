#!/usr/bin/env bash
# A handoff says, one line each, whether Jev's staffing advice and review
# triage ran. Unavailable advice leaves the prompt identical to "off", so only
# these lines, outside the prompt, tell the human.
set -uo pipefail
. "$(cd "$(dirname "$0")" && pwd)/lib/records.sh"
HANDOFF="${1:-$(cd "$(dirname "$0")/.." && pwd)/bin/tandem-handoff}"
[ -x "$HANDOFF" ] || { echo "not executable: $HANDOFF" >&2; exit 2; }

PASS=0; FAIL=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }
has()  { case "$OUT" in *"$2"*) ok "$1" ;; *) nope "$1 (missing: $2)" ;; esac; }
hasno() { case "$OUT" in *"$2"*) nope "$1 (present: $2)" ;; *) ok "$1" ;; esac; }

TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
. "$(cd "$(dirname "$0")" && pwd)/lib/stub-handoff.sh"
stub_handoff_env "$TMP"
REPO="$TMP/repo"; MB="$REPO/.tandem-relay"
mkdir -p "$REPO/docs/plans" "$MB"
PLAN="$REPO/docs/plans/p.md"; printf '# plan\n' > "$PLAN"

# `decision assess` writes STUB_DECISION and STUB_REASON to its --status file
# and prints advice only for "advice".
cat > "$TMP/bin/tandem.extra" <<'EXTRA'
if [ "$1 $2" = "decision assess" ]; then
  st=""; prev=""
  for a in "$@"; do [ "$prev" = --status ] && st="$a"; prev="$a"; done
  [ -n "$st" ] && printf '%s\n%s\n' "${STUB_DECISION:-advice}" "${STUB_REASON:-}" > "$st"
  [ "${STUB_DECISION:-advice}" = advice ] && printf '<staffing-advice>stub</staffing-advice>\n'
  exit 0
fi
EXTRA

run() {  # run [VAR=value...] -- a dry-run handoff; these lines print there too
  OUT=$(env -u HERDR_DECISION -u HERDR_JUDGE PATH="$TMP/bin:$PATH" HERDR_ENV=1 REPO="$REPO" \
        "$@" "$HANDOFF" "$PLAN" --dry-run 2>&1)
}
prompt() { printf '%s\n' "$OUT" | awk '/^--- prompt that would be sent ---$/{f=1;next} /^--- end ---$/{f=0} f'; }

printf '== staffing advice ==\n'
run
has "off: says so and why" "staffing advice: off (HERDR_DECISION is not 1)"
run HERDR_DECISION=1 STUB_DECISION=advice
has "advice: says it was added" "staffing advice: added"
case "$(prompt)" in *"<staffing-advice>"*) ok "advice: reaches the prompt" ;; *) nope "advice: reaches the prompt" ;; esac
run HERDR_DECISION=1 STUB_DECISION=no_advice
has "no_advice: says Jev answered and why nothing was added" "staffing advice: none (Jev answered but was not confident"
case "$(prompt)" in *"staffing-advice"*) nope "no_advice: prompt carries no advisory" ;; *) ok "no_advice: prompt carries no advisory" ;; esac
run HERDR_DECISION=1 STUB_DECISION=off
has "no key: warns and names the variable" "TYPESAFE_API_KEY"
has "no key: is a warning" "WARNING staffing advice"
run HERDR_DECISION=1 STUB_DECISION=error STUB_REASON="jev: request timed out"
has "error: warns with the reason" "jev: request timed out"

printf '\n== review triage ==\n'
run
has "off: says so and why" "review triage: off (HERDR_JUDGE is not 1)"

case "$(prompt)" in *"Tandem decides"*) nope "off: prompt has no triage instructions" ;; *) ok "off: prompt has no triage instructions" ;; esac
case "$(prompt)" in *"The reviewer pane reviews each task"*"not review the task yourself or spawn a reviewer subagent"*) ok "off: the builder is told the reviewer pane reviews, not it" ;; *) nope "off: the builder is told the reviewer pane reviews, not it" ;; esac
OFF_PROMPT=$(prompt)
write_builder "$MB"
run HERDR_JUDGE=1 TYPESAFE_API_KEY=k
has "enabled: tandem calls Jev" "review triage: tandem calls Jev"
case "$(prompt)" in *"Tandem decides when a task gets a review"*) ok "enabled: tandem controls reviews" ;; *) nope "enabled: tandem controls reviews" ;; esac
case "$(prompt)" in *"The reviewer pane reviews each task"*"not review the task yourself"*) ok "enabled: the builder is told the reviewer pane reviews, not it" ;; *) nope "enabled: the builder is told the reviewer pane reviews, not it" ;; esac
REMOVED=$(diff <(printf '%s\n' "$OFF_PROMPT") <(prompt) | grep -c '^<' || true)
[ "$REMOVED" -eq 0 ] && ok "enabled prompt only adds triage instructions" || nope "enabled prompt changed existing instructions"
run HERDR_JUDGE=1 TYPESAFE_API_KEY=
has "no key: warns and names the variable" "review triage: off (TYPESAFE_API_KEY is not set)"
[ "$(prompt)" = "$OFF_PROMPT" ] && ok "no key: prompt is identical to off" || nope "no key: prompt differs from off"
run STUB_BUILD_JUDGE=true
has "stored on: survives an unset environment" "Tandem decides when a task gets a review"
run HERDR_JUDGE=1 TYPESAFE_API_KEY=k STUB_BUILD_JUDGE=false
case "$(prompt)" in *"Tandem decides"*) nope "stored off: ignores the environment" ;; *) ok "stored off: ignores the environment" ;; esac

printf '\n%s passed, %s failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

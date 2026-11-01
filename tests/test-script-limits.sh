#!/usr/bin/env bash
# Limits on the scripts roadmap item 5 has moved: they make no jq call, since
# tandem answers their questions; each file is under 300 lines; and no message
# uses an em dash. Item 5 adds each script as it moves, and its last step
# checks all of bin/ and drops jq from install.sh.
set -uo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT" || exit 2

PASS=0; FAIL=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }

for f in bin/tandem-pair bin/lib/pair-*.sh bin/lib/relay.sh bin/tandem-unpair bin/lib/unpair-*.sh \
         bin/tandem-handoff bin/lib/handoff-*.sh bin/lib/handoff-prompt.txt bin/tandem-project \
         bin/tandem-handback bin/lib/handback-*.sh bin/lib/handback-prompt.txt \
         bin/tandem-ask bin/lib/ask-mailbox.sh bin/tandem-review bin/lib/review-*.sh bin/lib/review-*.txt \
         bin/tandem-bridge bin/lib/bridge-*.sh bin/tandem-status bin/tandem-verdict bin/tandem-init bin/tandem-relaylog; do
  # A jq word on a line that is not a comment.
  hits=$(grep -nE '(^|[^[:alnum:]_-])jq([^[:alnum:]_-]|$)' "$ROOT/$f" | grep -vE '^[0-9]+:[[:space:]]*#')
  if [ -z "$hits" ]; then
    ok "$f calls no jq"
  else
    nope "$f calls jq:"; printf '%s\n' "$hits" | sed 's/^/       | /'
  fi
done

for f in bin/tandem-pair bin/tandem-unpair bin/tandem-handoff bin/tandem-project bin/tandem-handback bin/tandem-ask \
         bin/tandem-review bin/tandem-bridge bin/tandem-status bin/tandem-verdict bin/tandem-init bin/tandem-relaylog bin/lib/*.sh bin/lib/*.txt; do
  n=$(wc -l < "$ROOT/$f" | tr -d ' ')
  [ "$n" -lt 300 ] && ok "$f is under 300 lines" || nope "$f has $n lines"
done

for f in bin/tandem-pair bin/lib/pair-*.sh bin/lib/relay.sh bin/tandem-unpair bin/lib/unpair-*.sh \
         bin/tandem-handoff bin/lib/handoff-*.sh bin/lib/handoff-prompt.txt \
         bin/tandem-handback bin/lib/handback-*.sh bin/lib/handback-prompt.txt bin/tandem-ask bin/lib/ask-mailbox.sh \
         bin/tandem-review bin/lib/review-*.sh bin/lib/review-*.txt bin/tandem-bridge bin/lib/bridge-*.sh \
         bin/tandem-status bin/tandem-verdict bin/tandem-init bin/tandem-relaylog; do
  [ -f "$ROOT/$f" ] || continue
  hits=$(grep -n '—' "$ROOT/$f" | grep -vE '^[0-9]+:[[:space:]]*#')
  [ -z "$hits" ] && ok "$f prints no em dash" \
    || { nope "$f prints an em dash:"; printf '%s\n' "$hits" | sed 's/^/       | /'; }
done

printf '\nscript-limits: passed %d, failed %d\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

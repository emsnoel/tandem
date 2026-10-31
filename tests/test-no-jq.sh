#!/usr/bin/env bash
# The scripts listed here make no jq call: tandem answers their questions.
# Roadmap item 5 adds each script as it moves, and its last step checks all of
# bin/ and drops jq from install.sh.
set -uo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)

PASS=0; FAIL=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }

for f in bin/tandem-pair; do
  # A jq word on a line that is not a comment.
  hits=$(grep -nE '(^|[^[:alnum:]_-])jq([^[:alnum:]_-]|$)' "$ROOT/$f" | grep -vE '^[0-9]+:[[:space:]]*#')
  if [ -z "$hits" ]; then
    ok "$f calls no jq"
  else
    nope "$f calls jq:"; printf '%s\n' "$hits" | sed 's/^/       | /'
  fi
done

printf '\nno-jq: passed %d, failed %d\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

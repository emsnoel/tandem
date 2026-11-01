#!/usr/bin/env bash
# Limits on every script in bin/: none calls jq, since tandem answers their
# questions; each file is under 300 lines; and no message uses an em dash.
# install.sh is held to the jq and em dash rules too, so install never needs
# jq. tests/test-install-no-jq.sh installs on a PATH without it.
set -uo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT" || exit 2

PASS=0; FAIL=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }

for f in bin/* bin/lib/* install.sh; do
  [ -f "$ROOT/$f" ] || continue
  # A jq word on a line that is not a comment.
  hits=$(grep -nE '(^|[^[:alnum:]_-])jq([^[:alnum:]_-]|$)' "$ROOT/$f" | grep -vE '^[0-9]+:[[:space:]]*#')
  if [ -z "$hits" ]; then
    ok "$f calls no jq"
  else
    nope "$f calls jq:"; printf '%s\n' "$hits" | sed 's/^/       | /'
  fi
done

for f in bin/* bin/lib/*; do
  [ -f "$ROOT/$f" ] || continue
  n=$(wc -l < "$ROOT/$f" | tr -d ' ')
  [ "$n" -lt 300 ] && ok "$f is under 300 lines" || nope "$f has $n lines"
done

for f in bin/* bin/lib/* install.sh; do
  [ -f "$ROOT/$f" ] || continue
  hits=$(grep -n '—' "$ROOT/$f" | grep -vE '^[0-9]+:[[:space:]]*#')
  [ -z "$hits" ] && ok "$f prints no em dash" \
    || { nope "$f prints an em dash:"; printf '%s\n' "$hits" | sed 's/^/       | /'; }
done

printf '\nscript-limits: passed %d, failed %d\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

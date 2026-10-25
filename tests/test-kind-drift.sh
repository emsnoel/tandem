#!/usr/bin/env bash
# The Go kind registry (internal/agent) is the source of truth for supported
# kinds. This catches hand-written kind lists in tandem-pair's help, README.md
# and tests/test-cli-drift.sh that drift from it.
#
# Hermetic: builds tandem from this tree, so `tandem agent kinds` reflects
# this checkout.
set -uo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT" || exit 2

PASS=0; FAIL=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }

TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
GOCACHE="${GOCACHE_OVERRIDE:-$ROOT/.gocache}" GOMODCACHE="${GOMODCACHE_OVERRIDE:-$ROOT/.gocache/mod}" \
  go build -o "$TMP/tandem" ./cmd/tandem \
  || { echo "could not build tandem for the fixture" >&2; exit 2; }

# One kind per line, sorted by Kinds().
KINDS=$("$TMP/tandem" agent kinds)
[ -n "$KINDS" ] || { echo "tandem agent kinds printed nothing" >&2; exit 2; }

in_set() { printf '%s\n' "$2" | grep -qxF "$1"; } # $1=value $2=newline-separated set
missing_from() { # $1 = candidate set (newline-separated); kinds in $KINDS absent from it
  local k out=""
  for k in $KINDS; do
    in_set "$k" "$1" || out="$out $k"
  done
  printf '%s' "$out"
}
extra_in() { # $1 = candidate set; tokens in it that are not real kinds
  local tok out=""
  for tok in $1; do
    in_set "$tok" "$KINDS" || out="$out $tok"
  done
  printf '%s' "$out"
}

extract_backtick_kinds() { # $1=file $2=grep anchor(fixed string) $3=tokens to exclude (space-separated)
  local file="$1" anchor="$2" exclude="$3" lineno line tok
  lineno=$(grep -n -F "$anchor" "$file" | head -1 | cut -d: -f1)
  [ -n "$lineno" ] || { printf 'MISSING_ANCHOR'; return; }
  line=$(sed -n "${lineno}p" "$file")
  for tok in $(printf '%s\n' "$line" | grep -oE '`[a-zA-Z]+`' | tr -d '`'); do
    case " $exclude " in
      *" $tok "*) continue ;;
    esac
    printf '%s\n' "$tok"
  done
}

echo "--- bin/tandem-pair's --builder help text ---"
pair_lineno=$(grep -n -- '--builder KIND' "$ROOT/bin/tandem-pair" | head -1 | cut -d: -f1)
if [ -z "$pair_lineno" ]; then
  nope "could not find the --builder KIND help line in bin/tandem-pair -- has it moved or been reworded? update this test's anchor"
else
  pair_line=$(sed -n "${pair_lineno}p" "$ROOT/bin/tandem-pair")
  pair_inside=$(printf '%s' "$pair_line" | sed -E 's/.*\(([^()]*)\).*/\1/')
  pair_kinds=$(printf '%s' "$pair_inside" | tr '|' '\n' | sed '/^$/d')
  m=$(missing_from "$pair_kinds"); e=$(extra_in "$pair_kinds")
  if [ -z "$m" ] && [ -z "$e" ]; then
    ok "bin/tandem-pair:$pair_lineno lists exactly the registered kinds"
  else
    nope "bin/tandem-pair:$pair_lineno is out of sync (missing:$m extra:$e) -- update its --builder KIND help text"
  fi
fi

echo "--- README.md's top-level prose ---"
r1=$(extract_backtick_kinds "$ROOT/README.md" "can be any supported agent CLI" "")
if [ "$r1" = "MISSING_ANCHOR" ]; then
  nope "could not find README.md's 'can be any supported agent CLI' line -- has it been reworded? update this test's anchor"
else
  m=$(missing_from "$r1"); e=$(extra_in "$r1")
  if [ -z "$m" ] && [ -z "$e" ]; then
    ok "README.md's top-level prose lists exactly the registered kinds"
  else
    nope "README.md's top-level prose is out of sync (missing:$m extra:$e)"
  fi
fi

echo "--- README.md's 'Choosing an agent' prose ---"
r2=$(extract_backtick_kinds "$ROOT/README.md" '`kind` selects' "kind")
if [ "$r2" = "MISSING_ANCHOR" ]; then
  nope "could not find README.md's '\`kind\` selects' line -- has it been reworded? update this test's anchor"
else
  m=$(missing_from "$r2"); e=$(extra_in "$r2")
  if [ -z "$m" ] && [ -z "$e" ]; then
    ok "README.md's 'Choosing an agent' prose lists exactly the registered kinds"
  else
    nope "README.md's 'Choosing an agent' prose is out of sync (missing:$m extra:$e)"
  fi
fi

echo "--- tests/test-cli-drift.sh has a probe line for every kind ---"
missing_probe=""
for k in $KINDS; do
  grep -qE "^probe[[:space:]]+$k([[:space:]]|\$)" "$ROOT/tests/test-cli-drift.sh" || missing_probe="$missing_probe $k"
done
if [ -z "$missing_probe" ]; then
  ok "every registered kind has a probe line in tests/test-cli-drift.sh"
else
  nope "tests/test-cli-drift.sh has no probe line for:$missing_probe -- add one so a new kind's CLI flags are still checked"
fi

echo "--- sanity: the registry is non-empty ---"
kcount=$(printf '%s\n' "$KINDS" | sed '/^$/d' | wc -l | tr -d ' ')
[ "$kcount" -ge 1 ] && ok "tandem agent kinds reports at least one kind ($kcount)" \
                     || nope "tandem agent kinds reported zero kinds"

printf '\nkind-drift: passed %d, failed %d\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

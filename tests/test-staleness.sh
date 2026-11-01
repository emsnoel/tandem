#!/usr/bin/env bash
# The installed tandem binary must not drift from the Go source. tandem-unpair
# runs tests/run.sh before teardown, so a stale binary blocks it.
set -uo pipefail
ROOT="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
PASS=0; FAIL=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }

# The same helper install.sh stamps with.
source_hash() { "$ROOT/bin/lib/source-hash.sh" "$ROOT"; }

WANT=$(source_hash)
# An empty hash would make the comparison pass vacuously.
if [ $? -ne 0 ] || [ -z "$WANT" ]; then
  nope "no build inputs found under $ROOT — staleness cannot be checked"
  printf '\nstaleness: passed %d, failed %d\n' "$PASS" "$FAIL"
  exit 1
fi
ok "source hash is computable"

if command -v tandem >/dev/null 2>&1; then
  GOT=$(tandem version --hash 2>/dev/null)
  if [ "$GOT" = "$WANT" ]; then
    ok "installed tandem matches the Go source"
  else
    nope "installed tandem is STALE — re-run ./install.sh --from-source (want $WANT, got ${GOT:-none})"
  fi
else
  ok "tandem not installed; skipping match (build-only checkout)"
fi

# --- a stale binary must say so clearly ---
# The scripts are symlinked and always current, while the binary can lag, so
# the scripts must name the remedy when a verb they need is missing.
STUB=$(mktemp -d); trap 'rm -rf "$STUB"' EXIT
if command -v tandem >/dev/null 2>&1 && command -v tandem-project >/dev/null 2>&1; then
  REAL=$(command -v tandem)
  cat > "$STUB/tandem" <<STUBEOF
#!/usr/bin/env bash
# Stands in for a tandem built before \`tandem config project\` existed.
if [ "\$1 \$2" = "config project" ]; then
  echo 'tandem config: unknown subcommand "project"' >&2
  exit 2
fi
exec "$REAL" "\$@"
STUBEOF
  chmod +x "$STUB/tandem"
  REPO=$(mktemp -d); git -C "$REPO" init -q
  OUT=$(cd "$REPO" && PATH="$STUB:$PATH" tandem-project --root . 2>&1); RC=$?

  [ "$RC" -ne 0 ] && ok "a tandem without \`config project\` refuses instead of guessing settings" \
    || nope "a tandem without \`config project\` refuses instead of guessing settings (exit $RC)"
  case "$OUT" in
    *install.sh*) ok "and names install.sh as the remedy" ;;
    *)            nope "and names install.sh as the remedy (got: $OUT)" ;;
  esac
  # Fail closed: a half-initialised repo would leave an unresolvable manifest.
  if [ -e "$REPO/.tandem.json" ] || [ -e "$REPO/.herdr.json" ]; then
    nope "and writes nothing"
  else
    ok "and writes nothing"
  fi
  rm -rf "$REPO"
else
  ok "tandem or tandem-project not installed; skipping the stale-binary message check"
fi

printf '\nstaleness: passed %d, failed %d\n' "$PASS" "$FAIL"; [ "$FAIL" -eq 0 ]

#!/usr/bin/env bash
# An undelivered plan must never look like a delivered one.
#
# When the socket is unreachable, tandem-handoff must say the plan was not
# delivered and exit 3, the code tandem-ask also reads.
#
# Hermetic: tandem is stubbed to fail as an unreachable socket does.
set -uo pipefail
HANDOFF="${1:-$(cd "$(dirname "$0")/.." && pwd)/bin/tandem-handoff}"
[ -x "$HANDOFF" ] || { echo "not executable: $HANDOFF" >&2; exit 2; }

PASS=0; FAIL=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }
chk()  { case "$OUT" in *"$2"*) ok "$1" ;; *) nope "$1 (missing: $2)" ;; esac; }

TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
mkdir -p "$TMP/bin"

# Exit 3 means the socket is unreachable. Every call is logged, so the test
# can prove nothing was tried afterwards.
cat > "$TMP/bin/tandem" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$MARKER"
exit 3
STUB
# Only for tandem-handoff's `command -v herdr` check; never run.
printf '#!/usr/bin/env bash\nprintf "{}"\n' > "$TMP/bin/herdr"
chmod +x "$TMP/bin/tandem" "$TMP/bin/herdr"

MARKER="$TMP/calls"; : > "$MARKER"
rc=0
OUT=$(PATH="$TMP/bin:$PATH" HERDR_ENV=1 MARKER="$MARKER" "$HANDOFF" 2>&1) || rc=$?

[ "$rc" -eq 3 ] && ok "exits 3, not 1" || nope "exits 3, not 1 (got $rc)"
chk "says the plan was not delivered"  "THE PLAN WAS NOT DELIVERED"
chk "says why it cannot fall back"     "socket-only"
chk "names the likely cause"           "Herdr server is running"

if grep -q 'agent prompt' "$MARKER"; then
  nope "never attempts delivery"
else
  ok "never attempts delivery"
fi

echo; printf 'handoff-unreachable: passed %d, failed %d\n' "$PASS" "$FAIL"; [ "$FAIL" -eq 0 ]

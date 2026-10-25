#!/usr/bin/env bash
# tandem-ask's default timeout depends on the direction. Asking the builder
# stays under Claude Code's 120s Bash limit; asking the planner allows time to
# read code.
#
# Hermetic: herdr and tandem are stubbed, since tandem-unpair runs this suite
# while a builder is live.
set -uo pipefail
ASK="${1:-$(cd "$(dirname "$0")/.." && pwd)/bin/tandem-ask}"
[ -x "$ASK" ] || { echo "not executable: $ASK" >&2; exit 2; }

PASS=0; FAIL=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }
chk()  { case "$OUT" in *"$2"*) ok "$1" ;; *) nope "$1 (missing: $2)" ;; esac; }

TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
mkdir -p "$TMP/bin"

# The socket is reachable, so tandem-ask takes the direct path and --dry-run
# prints the timeout it would use.
cat > "$TMP/bin/tandem" <<'STUB'
#!/usr/bin/env bash
case "$1 $2" in
  "agent snapshot") printf 'p9\t/repo\tidle\n' ;;
  *) exit 3 ;;
esac
STUB
cat > "$TMP/bin/herdr" <<'STUB'
#!/usr/bin/env bash
case "$1 $2" in
  "agent list") printf '{"result":{"agents":[{"pane_id":"pX","name":"peer","agent":"codex","agent_status":"idle","cwd":"/repo"}]}}' ;;
  *) printf '{}' ;;
esac
STUB
chmod +x "$TMP/bin/tandem" "$TMP/bin/herdr"

run_ask() { OUT=$(PATH="$TMP/bin:$PATH" HERDR_ENV=1 HERDR_PANE_ID=pX \
  "$@" "$ASK" --dry-run "${TARGET}" "q" 2>&1); }

echo "--- direction-aware defaults ---"
TARGET=planner run_ask env
chk "asking the planner allows 300s" "--timeout 300000"

TARGET=builder run_ask env
chk "asking the builder stays under Claude Code's 120s Bash limit" "--timeout 90000"

echo "--- explicit values still win ---"
TARGET=planner
OUT=$(PATH="$TMP/bin:$PATH" HERDR_ENV=1 HERDR_PANE_ID=pX \
  "$ASK" --dry-run --timeout 42 planner "q" 2>&1)
chk "--timeout overrides the default" "--timeout 42000"

OUT=$(PATH="$TMP/bin:$PATH" HERDR_ENV=1 HERDR_PANE_ID=pX \
  HERDR_ASK_TIMEOUT_SECONDS=77 "$ASK" --dry-run planner "q" 2>&1)
chk "HERDR_ASK_TIMEOUT_SECONDS overrides the default" "--timeout 77000"

OUT=$(PATH="$TMP/bin:$PATH" HERDR_ENV=1 HERDR_PANE_ID=pX \
  HERDR_ASK_TIMEOUT_SECONDS=77 "$ASK" --dry-run --timeout 42 planner "q" 2>&1)
chk "--timeout beats the environment variable" "--timeout 42000"

echo "--- the help text must not lie ---"
OUT=$("$ASK" --help 2>&1)
chk "help names the planner default" "300"
chk "help names the builder default" "90"

echo; printf 'ask-timeout: passed %d, failed %d\n' "$PASS" "$FAIL"; [ "$FAIL" -eq 0 ]

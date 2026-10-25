#!/usr/bin/env bash
# `tandem <verb>` must behave exactly like running tandem-<verb> directly.
#
# Dispatch ends in execve, so it can only be tested from outside. The exit code
# is what matters: tandem-unpair relies on exact codes (3, 22, 23), and a
# dispatcher that re-exited could flatten them to 1.
#
# Hermetic: builds its own binary and delegate in a temp dir.
set -uo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
PASS=0; FAIL=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }

command -v go >/dev/null 2>&1 || { echo "  FAIL go is not on PATH" >&2; exit 1; }

TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
BIN="$TMP/bin"; mkdir -p "$BIN"

# Build before PATH is narrowed.
TANDEM="$TMP/tandem"
( cd "$ROOT" && go build -o "$TANDEM" ./cmd/tandem ) \
  || { echo "  FAIL could not build cmd/tandem" >&2; exit 1; }

# The delegate records how it was called and exits with the code it is given.
cat > "$BIN/tandem-probe" <<'PROBE'
#!/usr/bin/env bash
printf '%s\n' "$0" > "$PROBE_ARGV0"
: > "$PROBE_ARGS"
for a in "$@"; do printf '%s\n' "$a" >> "$PROBE_ARGS"; done
exit "${PROBE_EXIT:-0}"
PROBE
chmod +x "$BIN/tandem-probe"

export PROBE_ARGV0="$TMP/argv0" PROBE_ARGS="$TMP/args"

# run <expected-exit> <verb> [args...]: invoke through the dispatcher with only
# the fake delegate on PATH.
run() {
  local want="$1"; shift
  PATH="$BIN:/usr/bin:/bin" "$TANDEM" "$@" >/dev/null 2>&1
  local got=$?
  [ "$got" = "$want" ]
}

# --- the handover reaches the delegate at all --------------------------------
if run 0 probe; then ok "dispatches tandem probe to the delegate"
else nope "dispatches tandem probe to the delegate"; fi

# --- argv[0] names the command a human would type ---
if [ "$(basename "$(cat "$PROBE_ARGV0" 2>/dev/null)")" = "tandem-probe" ]; then
  ok "argv[0] is tandem-probe, not tandem"
else
  nope "argv[0] is tandem-probe (got $(cat "$PROBE_ARGV0" 2>/dev/null))"
fi

# --- arguments arrive verbatim ---
# Including flag-like, empty and space-containing ones.
run 0 probe --root . --flag "two words" "" -x
printf '%s\n' "--root" "." "--flag" "two words" "" "-x" > "$TMP/args.want"
if diff -q "$TMP/args.want" "$PROBE_ARGS" >/dev/null 2>&1; then
  ok "forwards arguments verbatim, spaces and empties included"
else
  nope "forwards arguments verbatim"; diff "$TMP/args.want" "$PROBE_ARGS" | sed 's/^/       /'
fi

# --- every exit code survives ---
for code in 0 1 2 3 20 22 23 42; do
  if PROBE_EXIT="$code" run "$code" probe; then ok "exit $code passes through unchanged"
  else nope "exit $code passes through unchanged"; fi
done

# --- a compiled subcommand is never shadowed by a script ---
cp "$BIN/tandem-probe" "$BIN/tandem-version"
if PROBE_EXIT=99 PATH="$BIN:/usr/bin:/bin" "$TANDEM" version >/dev/null 2>&1; then
  ok "a compiled subcommand wins over a same-named delegate"
else
  nope "a compiled subcommand wins over a same-named delegate (exit $?)"
fi
rm -f "$BIN/tandem-version"

# --- refusals ----------------------------------------------------------------
if run 2 nosuchverb; then ok "an unknown verb with no delegate is a usage error"
else nope "an unknown verb with no delegate is a usage error"; fi

# A verb is never a path.
if run 2 "../bin/tandem-probe"; then ok "a verb containing a path separator is refused"
else nope "a verb containing a path separator is refused"; fi

if run 2 -- --version; then ok "a verb leading with a dash is refused"
else nope "a verb leading with a dash is refused"; fi

# --- usage names both families ----------------------------------------------
USAGE=$(PATH="$BIN:/usr/bin:/bin" "$TANDEM" 2>&1)
case "$USAGE" in
  *version*) ok "usage lists the compiled subcommands" ;;
  *)         nope "usage lists the compiled subcommands" ;;
esac
case "$USAGE" in
  *probe*) ok "usage discovers delegates from PATH" ;;
  *)       nope "usage discovers delegates from PATH" ;;
esac

printf '\ndispatch: passed %d, failed %d\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

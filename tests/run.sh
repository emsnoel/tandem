#!/usr/bin/env bash
# run.sh: the project's check command. Exit 0 means the harness is sound.
#
# Every test is hermetic: no Herdr server, panes or network, and no writes
# outside a temp dir or the repo. The Go cache is inside the repo (.gocache/,
# ignored) because the builder's sandbox denies ~/Library/Caches.
#
# macOS bash 3.2 fails on an empty array under set -u, so this uses globs and
# a counter.
set -uo pipefail

# Jev is opt-in per machine, and tandem-unpair runs this check in that
# environment. lib/clean-env.sh clears the switches so a machine with Jev on
# still passes.
. "$(dirname "$0")/lib/clean-env.sh"

ROOT=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT" || exit 2

# The fake event socket the shell suites use (tests/lib/fake-socket.sh).
TANDEM_FAKE_SOCK_BIN="$ROOT/.gocache/bin/fakeherdrsock"
mkdir -p "$ROOT/.gocache/bin"
GOCACHE="${GOCACHE_OVERRIDE:-$ROOT/.gocache}" GOMODCACHE="${GOMODCACHE_OVERRIDE:-$ROOT/.gocache/mod}" \
  go build -o "$TANDEM_FAKE_SOCK_BIN" ./tests/fakeherdrsock || { echo "could not build tests/fakeherdrsock" >&2; exit 1; }
export TANDEM_FAKE_SOCK_BIN

WITH_CLIS=0
if [ "${1:-}" = "--with-clis" ]; then
  WITH_CLIS=1
  shift
fi
if [ "$#" -ne 0 ]; then
  printf 'usage: %s [--with-clis]\n' "$0" >&2
  exit 2
fi

FAILED=0
hdr() { printf '\n== %s ==\n' "$1"; }

hdr "shell syntax"
for f in bin/* bin/lib/*.sh tests/*.sh tests/lib/*.sh tools/*.sh; do
  [ -f "$f" ] || continue
  if bash -n "$f" 2>/dev/null; then
    printf '  ok   %s\n' "$f"
  else
    printf '  FAIL %s\n' "$f"; bash -n "$f" 2>&1 | sed 's/^/       /'
    FAILED=$((FAILED+1))
  fi
done

# --- go ---
hdr "go"
# Both caches stay inside the workspace for the builder's sandbox, which also
# denies ~/go/pkg/mod.
export GOCACHE="${GOCACHE_OVERRIDE:-$ROOT/.gocache}"
export GOMODCACHE="${GOMODCACHE_OVERRIDE:-$ROOT/.gocache/mod}"
if command -v go >/dev/null 2>&1; then
  if go build ./... 2>&1; then printf '  ok   go build\n'; else printf '  FAIL go build\n'; FAILED=$((FAILED+1)); fi
  if go test ./... 2>&1; then printf '  ok   go test\n';  else printf '  FAIL go test\n';  FAILED=$((FAILED+1)); fi
else
  printf '  FAIL go is not on PATH\n'; FAILED=$((FAILED+1))
fi

for t in tests/test-*.sh; do
  [ -f "$t" ] || continue
  if [ "$t" = "tests/test-cli-drift.sh" ] && [ "$WITH_CLIS" -eq 0 ]; then
    continue
  fi
  hdr "$(basename "$t" .sh)"
  # Name a suite that exits non-zero even if it printed no FAIL line.
  bash "$t"; rc=$?
  if [ "$rc" -ne 0 ]; then
    printf '  FAIL %s (exit %d)\n' "$(basename "$t" .sh)" "$rc"
    FAILED=$((FAILED+1))
  fi
done

printf '\n'
if [ "$FAILED" -eq 0 ]; then
  printf 'all checks passed\n'
else
  printf '%d check(s) FAILED\n' "$FAILED"
fi
exit $(( FAILED > 0 ))

#!/usr/bin/env bash
# run.sh: the project's check command. Exit 0 means the harness is sound.
#
#   ./tests/run.sh                    everything
#   ./tests/run.sh --with-clis        also checks the flags of installed agent CLIs
#   ./tests/run.sh --only NAME ...    shell syntax plus only the named suites
#                                     (test-kind-drift) or Go packages
#                                     (./internal/agent/...); repeatable
#   ./tests/run.sh --jobs N           run N shell suites at once
#
# Every test is hermetic: no Herdr server, panes or network, and no writes
# outside a temp dir or the repo. So the shell suites run in parallel, beside
# `go test`. The Go cache is inside the repo (.gocache/, ignored) because the
# builder's sandbox denies ~/Library/Caches.
#
# macOS bash 3.2 fails on an empty array under set -u, so arrays are expanded
# with ${a[@]+"${a[@]}"}.
set -uo pipefail

# Jev is opt-in per machine, and tandem-unpair runs this check in that
# environment. lib/clean-env.sh clears the switches so a machine with Jev on
# still passes.
. "$(dirname "$0")/lib/clean-env.sh"

ROOT=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT" || exit 2
. "$ROOT/tests/lib/run-suites.sh"

usage() { printf 'usage: %s [--with-clis] [--jobs N] [--only NAME]...\n' "$0" >&2; exit 2; }

WITH_CLIS=0
# Half the cores by default: several suites time real waits, and a machine
# packed full of suites and `go test` can stretch those past their limits.
CORES=$(getconf _NPROCESSORS_ONLN 2>/dev/null || echo 4)
JOBS=$(( CORES / 2 > 1 ? CORES / 2 : 2 ))
ONLY_SUITES=(); ONLY_PKGS=()
while [ "$#" -gt 0 ]; do
  case "$1" in
    --with-clis) WITH_CLIS=1 ;;
    --jobs) [ "$#" -ge 2 ] && [ "$2" -gt 0 ] 2>/dev/null || usage; JOBS=$2; shift ;;
    --only)
      [ "$#" -ge 2 ] || usage
      case "$2" in
        ./*|*/*) ONLY_PKGS+=("$2") ;;
        *) n=${2%.sh}; n=test-${n#test-}
           [ -f "tests/$n.sh" ] || { printf 'run.sh: no suite tests/%s.sh\n' "$n" >&2; exit 2; }
           ONLY_SUITES+=("tests/$n.sh") ;;
      esac
      shift ;;
    *) usage ;;
  esac
  shift
done
ONLY=0
[ -n "${ONLY_SUITES[*]+x}${ONLY_PKGS[*]+x}" ] && ONLY=1

# Both caches stay inside the workspace for the builder's sandbox, which also
# denies ~/go/pkg/mod.
export GOCACHE="${GOCACHE_OVERRIDE:-$ROOT/.gocache}"
export GOMODCACHE="${GOMODCACHE_OVERRIDE:-$ROOT/.gocache/mod}"

# The fake event socket the shell suites use (tests/lib/fake-socket.sh).
TANDEM_FAKE_SOCK_BIN="$ROOT/.gocache/bin/fakeherdrsock"
mkdir -p "$ROOT/.gocache/bin"
go build -o "$TANDEM_FAKE_SOCK_BIN" ./tests/fakeherdrsock || { echo "could not build tests/fakeherdrsock" >&2; exit 1; }
export TANDEM_FAKE_SOCK_BIN

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

# --- go, in the background while the shell suites run ---
GO_LOG=$(mktemp); GO_PID=""
trap 'rm -f "$GO_LOG"' EXIT
go_all() {
  if go build ./... 2>&1; then printf '  ok   go build\n'; else printf '  FAIL go build\n'; return 1; fi
  # internal/controller takes 6 to 10 minutes on a loaded machine, so Go's
  # 10-minute default per package fails it with a panic that is not a test failure.
  if go test -timeout 30m ./... 2>&1; then printf '  ok   go test\n'; else printf '  FAIL go test\n'; return 1; fi
}
go_only() {
  if go test -timeout 30m "$@" 2>&1; then printf '  ok   go test %s\n' "$*"; else printf '  FAIL go test %s\n' "$*"; return 1; fi
}
if ! command -v go >/dev/null 2>&1; then
  printf '  FAIL go is not on PATH\n' > "$GO_LOG"; echo 1 > "$GO_LOG.rc"
elif [ "$ONLY" -eq 0 ]; then
  ( go_all > "$GO_LOG" 2>&1; echo "$?" > "$GO_LOG.rc" ) & GO_PID=$!
elif [ -n "${ONLY_PKGS[*]+x}" ]; then
  ( go_only "${ONLY_PKGS[@]}" > "$GO_LOG" 2>&1; echo "$?" > "$GO_LOG.rc" ) & GO_PID=$!
fi

# --- shell suites ---
if [ "$ONLY" -eq 1 ]; then
  SUITES=(${ONLY_SUITES[@]+"${ONLY_SUITES[@]}"})
else
  SUITES=()
  for t in tests/test-*.sh; do
    [ -f "$t" ] || continue
    [ "$t" = "tests/test-cli-drift.sh" ] && [ "$WITH_CLIS" -eq 0 ] && continue
    SUITES+=("$t")
  done
fi
run_suites "$JOBS" ${SUITES[@]+"${SUITES[@]}"}

if [ -n "$GO_PID" ] || [ -f "$GO_LOG.rc" ]; then
  [ -n "$GO_PID" ] && wait "$GO_PID"
  hdr "go"
  cat "$GO_LOG"
  [ "$(cat "$GO_LOG.rc" 2>/dev/null || echo 1)" -eq 0 ] || FAILED=$((FAILED+1))
  rm -f "$GO_LOG.rc"
fi

printf '\n'
if [ "$FAILED" -eq 0 ]; then
  printf 'all checks passed\n'
else
  printf '%d check(s) FAILED\n' "$FAILED"
fi
exit $(( FAILED > 0 ))

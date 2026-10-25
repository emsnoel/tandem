# shellcheck shell=bash
# fake_socket_start LIFELINE: serve Herdr's event stream from the fake `herdr`
# on PATH (tests/fakeherdrsock) and export HERDR_SOCKET_PATH. The socket is
# under /tmp because macOS limits socket paths to 104 bytes. The server exits
# when LIFELINE, the test's temp dir, is removed. tests/run.sh builds it once
# (TANDEM_FAKE_SOCK_BIN); a suite run alone builds it into LIFELINE.
# Resolved at source time, before suites narrow PATH.
_TFS_GO=$(command -v go 2>/dev/null || true)

fake_socket_start() {
  local life="$1" dir bin="${TANDEM_FAKE_SOCK_BIN:-}" root
  if [ -z "$bin" ]; then
    root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
    bin="$life/fakeherdrsock"
    (cd "$root" && GOCACHE="${GOCACHE_OVERRIDE:-$root/.gocache}" GOMODCACHE="${GOMODCACHE_OVERRIDE:-$root/.gocache/mod}" \
      "${_TFS_GO:-go}" build -o "$bin" ./tests/fakeherdrsock) || { echo "could not build tests/fakeherdrsock" >&2; return 1; }
  fi
  dir=$(mktemp -d /tmp/tfs.XXXXXX) || return 1
  "$bin" "$dir/herdr.sock" "$life" >/dev/null 2>&1 &
  export HERDR_SOCKET_PATH="$dir/herdr.sock"
  # Wait up to 5 s for the socket, in 50 ms steps.
  local i=0
  while [ "$i" -lt 100 ]; do
    [ -S "$HERDR_SOCKET_PATH" ] && return 0
    sleep 0.05; i=$((i+1))
  done
  echo "  FAIL fake socket did not start"; return 1
}

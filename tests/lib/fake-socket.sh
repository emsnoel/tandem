# shellcheck shell=bash
# fake_socket_start LIFELINE: serve Herdr's event stream from the fake `herdr`
# on PATH (tests/fakeherdrsock) and export HERDR_SOCKET_PATH. The socket is
# placed in a short writable directory because macOS limits Unix socket paths
# to 104 bytes. The server exits when LIFELINE, the test's temp dir, is
# removed. tests/run.sh builds it once (TANDEM_FAKE_SOCK_BIN); a suite run
# alone builds it into LIFELINE.
#
# Inside a sandboxed builder (TANDEM_BUILD_CHECK=1), Unix socket binding may
# be denied even in writable directories. When every candidate directory fails
# with a permission error, fake_socket_start prints a skip line and exits 0 so
# the suite does not block a builder's local check; tandem's own check runs
# outside the sandbox.
# Resolved at source time, before suites narrow PATH or cd.
_TFS_GO=$(command -v go 2>/dev/null || true)
_TFS_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)

_tfs_try_dir() {
  local bin="$1" life="$2" dir="$3" errlog="$4" sock="$3/herdr.sock" pid i=0
  # macOS sun_path is 104 bytes including NUL; stay comfortably below it.
  if [ "${#sock}" -gt 100 ]; then
    rm -rf "$dir" 2>/dev/null || true
    return 2
  fi
  : > "$errlog"
  "$bin" "$sock" "$life" >/dev/null 2>"$errlog" &
  pid=$!
  export HERDR_SOCKET_PATH="$sock"
  # Wait up to 5 s for the socket, in 50 ms steps. Stop early if the server exits.
  while [ "$i" -lt 100 ]; do
    [ -S "$HERDR_SOCKET_PATH" ] && return 0
    kill -0 "$pid" 2>/dev/null || break
    sleep 0.05; i=$((i+1))
  done
  wait "$pid" 2>/dev/null || true
  rm -rf "$dir" 2>/dev/null || true
  return 1
}

fake_socket_start() {
  local life="$1" bin="${TANDEM_FAKE_SOCK_BIN:-}" root="${_TFS_ROOT}"
  if [ -z "$bin" ]; then
    bin="$life/fakeherdrsock"
    (cd "$root" && GOCACHE="${GOCACHE_OVERRIDE:-$root/.gocache}" GOMODCACHE="${GOMODCACHE_OVERRIDE:-$root/.gocache/mod}" \
      "${_TFS_GO:-go}" build -o "$bin" ./tests/fakeherdrsock) || { echo "could not build tests/fakeherdrsock" >&2; return 1; }
  fi

  local errlog="$life/.fakeherdrsock.err"
  local bases=() base dir
  if [ -n "${TMPDIR:-}" ]; then
    base="${TMPDIR%/}"
    [ -n "$base" ] && bases+=("$base")
  fi
  if [ "${#bases[@]}" -eq 0 ] || [ "${bases[0]}" != "/tmp" ]; then
    bases+=("/tmp")
  fi
  # Repo-local writable dir inside .gocache (gitignored) for sandboxes that
  # deny /tmp and $TMPDIR.
  if [ -d "$root/.gocache" ] || mkdir -p "$root/.gocache" 2>/dev/null; then
    bases+=("$root/.gocache")
  fi

  local saw_perm=0 last_err=""
  for base in ${bases[@]+"${bases[@]}"}; do
    [ -d "$base" ] && [ -w "$base" ] || continue
    dir=$(mktemp -d "$base/tfs.XXXXXX" 2>/dev/null) || continue
    if _tfs_try_dir "$bin" "$life" "$dir" "$errlog"; then
      rm -f "$errlog"
      return 0
    fi
    if [ -s "$errlog" ]; then
      last_err=$(head -n 1 "$errlog")
      case "$last_err" in
        *"operation not permitted"*|*"permission denied"*|*"Operation not permitted"*|*"Permission denied"*)
          saw_perm=1 ;;
      esac
    fi
  done

  # Final fallback: a short subdir inside LIFELINE itself.
  if [ -d "$life" ] && [ -w "$life" ]; then
    dir="$life/tfs"
    if mkdir -p "$dir" 2>/dev/null; then
      if _tfs_try_dir "$bin" "$life" "$dir" "$errlog"; then
        rm -f "$errlog"
        return 0
      fi
      if [ -s "$errlog" ]; then
        last_err=$(head -n 1 "$errlog")
        case "$last_err" in
          *"operation not permitted"*|*"permission denied"*|*"Operation not permitted"*|*"Permission denied"*)
            saw_perm=1 ;;
        esac
      fi
    fi
  fi
  rm -f "$errlog"

  if [ "$saw_perm" -eq 1 ] && [ "${TANDEM_BUILD_CHECK:-}" = 1 ]; then
    printf '  skip fake Herdr socket blocked by sandbox (%s)\n' "$last_err"
    exit 0
  fi
  if [ -n "$last_err" ]; then
    printf '  FAIL fake socket did not start (%s)\n' "$last_err"
  else
    echo "  FAIL fake socket did not start"
  fi
  return 1
}

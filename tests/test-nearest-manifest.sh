#!/usr/bin/env bash
# A monorepo package's own .tandem.json wins over one further up the tree.
# This runs the real walk: a nested git repo, the real tandem binary and the
# real bin/tandem-project.
#
# Hermetic: builds tandem from this tree.
set -uo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT" || exit 2

PASS=0; FAIL=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }

command -v jq >/dev/null || { echo "need jq" >&2; exit 2; }

TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
mkdir -p "$TMP/bin"
GOCACHE="${GOCACHE_OVERRIDE:-$ROOT/.gocache}" GOMODCACHE="${GOMODCACHE_OVERRIDE:-$ROOT/.gocache/mod}" \
  go build -o "$TMP/bin/tandem" ./cmd/tandem \
  || { echo "could not build tandem for the fixture" >&2; exit 2; }
ln -s "$ROOT/bin/tandem-project" "$TMP/bin/tandem-project"
ln -s "$(command -v jq)" "$TMP/bin/jq"

MONO="$TMP/mono"
mkdir -p "$MONO/packages/foo" "$MONO/packages/bar"
# Physical path: macOS /tmp is a symlink, and git resolves it.
MONO=$(cd "$MONO" && pwd -P)
git -C "$MONO" init -q
cat > "$MONO/.tandem.json" <<'JSON'
{ "check_command": "monorepo-root-check" }
JSON
cat > "$MONO/packages/foo/.tandem.json" <<'JSON'
{ "check_command": "foo-specific-check" }
JSON

run_from() { # $1 = directory to run tandem-project from, rest = extra args
  local dir="$1"; shift
  (cd "$dir" && PATH="$TMP/bin:$PATH" tandem-project "$@" 2>/dev/null)
}

# --- a package with its own manifest wins over the root's ---
OUT=$(run_from "$MONO/packages/foo")
[[ "$(jq -r '.check_command' <<<"$OUT")" == "foo-specific-check" ]] \
  && ok "a package's own manifest wins over the root's" \
  || nope "a package's own manifest wins over the root's (got $(jq -r '.check_command' <<<"$OUT"))"
[[ "$(jq -r '.root' <<<"$OUT")" == "$MONO" ]] \
  && ok "root still names the monorepo toplevel, not the package" \
  || nope "root still names the monorepo toplevel, not the package (got $(jq -r '.root' <<<"$OUT"))"

# --- a package with no manifest falls through to the root's ---
OUT=$(run_from "$MONO/packages/bar")
[[ "$(jq -r '.check_command' <<<"$OUT")" == "monorepo-root-check" ]] \
  && ok "a package with no manifest falls through to the root's" \
  || nope "a package with no manifest falls through to the root's (got $(jq -r '.check_command' <<<"$OUT"))"
[[ "$(jq -r '.root' <<<"$OUT")" == "$MONO" ]] \
  && ok "root still names the monorepo toplevel for the fallback case" \
  || nope "root still names the monorepo toplevel for the fallback case"

# --- run from the monorepo root itself ---
OUT=$(run_from "$MONO")
[[ "$(jq -r '.check_command' <<<"$OUT")" == "monorepo-root-check" ]] \
  && ok "run from the toplevel itself: uses the toplevel's own manifest" \
  || nope "run from the toplevel itself: uses the toplevel's own manifest"

# --- an explicit --root is the search start ---
OUT=$(run_from "$MONO" --root "$MONO/packages/foo")
[[ "$(jq -r '.check_command' <<<"$OUT")" == "foo-specific-check" ]] \
  && ok "explicit --root searches from the given directory, not cwd" \
  || nope "explicit --root searches from the given directory, not cwd"
[[ "$(jq -r '.root' <<<"$OUT")" == "$MONO/packages/foo" ]] \
  && ok "explicit --root is still echoed back verbatim as root" \
  || nope "explicit --root is still echoed back verbatim as root"

# --- a single repo, from a deep subdirectory ---
SINGLE="$TMP/single"
mkdir -p "$SINGLE/src/deep/nested"
SINGLE=$(cd "$SINGLE" && pwd -P)  # physical path -- see the MONO comment above
git -C "$SINGLE" init -q
cat > "$SINGLE/.tandem.json" <<'JSON'
{ "check_command": "single-repo-check" }
JSON
OUT=$(run_from "$SINGLE/src/deep/nested")
[[ "$(jq -r '.check_command' <<<"$OUT")" == "single-repo-check" ]] \
  && ok "single-repo: a deeply nested directory still finds the root manifest" \
  || nope "single-repo: a deeply nested directory still finds the root manifest"
[[ "$(jq -r '.root' <<<"$OUT")" == "$SINGLE" ]] \
  && ok "single-repo: root is still the toplevel, unchanged" \
  || nope "single-repo: root is still the toplevel, unchanged"

# --- not a git repository: single-directory lookup ---
NOGIT="$TMP/nogit"
mkdir -p "$NOGIT"
cat > "$NOGIT/.tandem.json" <<'JSON'
{ "check_command": "no-git-repo-check" }
JSON
OUT=$(run_from "$NOGIT")
[[ "$(jq -r '.check_command' <<<"$OUT")" == "no-git-repo-check" ]] \
  && ok "outside a git repo: still finds the manifest in the working directory" \
  || nope "outside a git repo: still finds the manifest in the working directory"

# --- a symlinked checkout must not escape to a decoy manifest ---
# git resolves symlinks but a logical $PWD does not, so a naive comparison
# would climb past the real root.
REALREPO="$TMP/symreal"
mkdir -p "$REALREPO/packages/foo"
REALREPO=$(cd "$REALREPO" && pwd -P)
git -C "$REALREPO" init -q
LINKREPO="$TMP/symlink"
ln -s "$REALREPO" "$LINKREPO"
# A decoy manifest one level above LINKREPO, outside REALREPO.
cat > "$TMP/.tandem.json" <<'JSON'
{ "check_command": "DECOY-OUTSIDE-THE-REPO" }
JSON
OUT=$(run_from "$LINKREPO/packages/foo")
[[ "$(jq -r '.check_command' <<<"$OUT")" != "DECOY-OUTSIDE-THE-REPO" ]] \
  && ok "symlinked checkout: never escapes to a decoy outside the repo" \
  || nope "symlinked checkout: never escapes to a decoy outside the repo (got the decoy)"
rm -f "$TMP/.tandem.json"

printf '\nnearest-manifest: passed %d, failed %d\n' "$PASS" "$FAIL"
(( FAIL == 0 ))

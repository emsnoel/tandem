#!/usr/bin/env bash
# tandem-project emits an optional check_paths array from .tandem.json, which
# limits gate 4's staleness check. Absent or wrong-typed gives [].
#
# Hermetic: tandem is stubbed for `paths manifest`.
set -uo pipefail
PROJECT="${1:-$(cd "$(dirname "$0")/.." && pwd)/bin/tandem-project}"
[ -x "$PROJECT" ] || { echo "not executable: $PROJECT" >&2; exit 2; }

PASS=0; FAIL=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }

command -v jq >/dev/null || { echo "need jq" >&2; exit 2; }

TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
mkdir -p "$TMP/bin" "$TMP/repo"
# Symlink, not copy: macOS kills a copied /usr/bin/jq on exec.
ln -s "$(command -v jq)" "$TMP/bin/jq"
cat > "$TMP/bin/tandem" <<'STUB'
#!/usr/bin/env bash
if [[ "$1" == "paths" && "$2" == "manifest" ]]; then
  shift 2
  while (( $# )); do
    # tandem-project calls `paths manifest --from`; the walk itself is tested in
    # test-nearest-manifest.sh.
    [[ "$1" == --from ]] && { printf '%s/.tandem.json\n' "$2"; exit 0; }
    shift
  done
  exit 1
fi
exit 1
STUB
chmod +x "$TMP/bin"/*
export PATH="$TMP/bin:$PATH"

run() { "$PROJECT" --root "$TMP/repo" 2>/dev/null; }

# --- absent key: whole tree ---------------------------------------------------
cat > "$TMP/repo/.tandem.json" <<'JSON'
{ "check_command": "true" }
JSON
GOT=$(jq -c '.check_paths' <<<"$(run)")
[[ "$GOT" == "[]" ]] && ok "absent check_paths defaults to []" || nope "absent check_paths defaults to [] (got $GOT)"

# --- declared array: passed through verbatim ----------------------------------
cat > "$TMP/repo/.tandem.json" <<'JSON'
{ "check_command": "true", "check_paths": ["src", "internal"] }
JSON
GOT=$(jq -c '.check_paths' <<<"$(run)")
[[ "$GOT" == '["src","internal"]' ]] && ok "declared check_paths passed through" || nope "declared check_paths passed through (got $GOT)"

# --- wrong type: falls back to [] ---
cat > "$TMP/repo/.tandem.json" <<'JSON'
{ "check_command": "true", "check_paths": "not-an-array" }
JSON
GOT=$(jq -c '.check_paths' <<<"$(run)")
[[ "$GOT" == "[]" ]] && ok "wrong-typed check_paths falls back to []" || nope "wrong-typed check_paths falls back to [] (got $GOT)"

printf '\ntandem-project check_paths: passed %d, failed %d\n' "$PASS" "$FAIL"
(( FAIL == 0 ))

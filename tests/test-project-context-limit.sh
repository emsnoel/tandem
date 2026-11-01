#!/usr/bin/env bash
# tandem-project emits context_limit from .tandem.json, the most prompt bytes a
# durable run gives the builder for one task. Absent or not a whole number
# gives 0, the engine default.
#
# Hermetic: tandem is stubbed for `paths manifest`. The real binary answers
# `config check-command`.
set -uo pipefail
PROJECT="${1:-$(cd "$(dirname "$0")/.." && pwd)/bin/tandem-project}"
[ -x "$PROJECT" ] || { echo "not executable: $PROJECT" >&2; exit 2; }

PASS=0; FAIL=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }

command -v jq >/dev/null || { echo "need jq" >&2; exit 2; }

export REAL_TANDEM; REAL_TANDEM=$(command -v tandem) || { echo "need an installed tandem" >&2; exit 2; }

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
[[ "$1 $2" == "config check-command" ]] && exec "$REAL_TANDEM" "$@"
exit 1
STUB
chmod +x "$TMP/bin"/*
export PATH="$TMP/bin:$PATH"

run() { "$PROJECT" --root "$TMP/repo" 2>/dev/null; }

limit_for() { # limit_for JSON -> the context_limit tandem-project reports
  printf '%s\n' "$1" > "$TMP/repo/.tandem.json"
  jq -c '.context_limit' <<<"$(run)"
}

GOT=$(limit_for '{ "check_command": "true" }')
[[ "$GOT" == 0 ]] && ok "absent context_limit is 0, the engine's default" || nope "absent context_limit (got $GOT)"
GOT=$(limit_for '{ "check_command": "true", "context_limit": 20000 }')
[[ "$GOT" == 20000 ]] && ok "a declared context_limit is passed through" || nope "declared context_limit (got $GOT)"
GOT=$(limit_for '{ "check_command": "true", "context_limit": "big" }')
[[ "$GOT" == 0 ]] && ok "a context_limit that is not a number falls back to 0" || nope "wrong-typed context_limit (got $GOT)"
GOT=$(limit_for '{ "check_command": "true", "context_limit": -5 }')
[[ "$GOT" == 0 ]] && ok "a negative context_limit falls back to 0" || nope "negative context_limit (got $GOT)"

printf '\ntandem-project context_limit: passed %d, failed %d\n' "$PASS" "$FAIL"
(( FAIL == 0 ))

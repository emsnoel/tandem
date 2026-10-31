#!/usr/bin/env bash
# tandem-pair labels each pane "role (kind)" and names the planner's tab
# "tandem: <repo>" while it still has Herdr's default number.
#
# Hermetic: a stub herdr and --dry-run, so no pane is touched.
set -uo pipefail
PAIR="${1:-$(cd "$(dirname "$0")/.." && pwd)/bin/tandem-pair}"
[ -x "$PAIR" ] || { echo "not executable: $PAIR" >&2; exit 2; }

PASS=0; FAIL=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }
SHOWN=0
# On the first miss, show the output; a refusal otherwise reads as a missing
# label.
chk()  { case "$OUT" in *"$2"*) ok "$1" ;; *) nope "$1 (missing: $2)"
           [ "$SHOWN" -eq 1 ] || { SHOWN=1; printf '%s\n' "$OUT" | sed 's/^/       | /'; } ;; esac; }
lack() { case "$OUT" in *"$2"*) nope "$1 (found: $2)" ;; *) ok "$1" ;; esac; }

command -v jq >/dev/null || { echo "need jq" >&2; exit 2; }

TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
mkdir -p "$TMP/bin" "$TMP/labelled-repo"
ln -s "$(command -v jq)" "$TMP/bin/jq"
cat > "$TMP/bin/herdr" <<'STUB'
#!/usr/bin/env bash
case "$1 $2" in
  "agent list")
    printf '{"result":{"agents":[{"pane_id":"w1:p1","agent":"claude","name":"","agent_status":"idle","cwd":"%s","tab_id":"w1:t1"}]}}\n' "$REPO" ;;
  "tab get")
    printf '{"result":{"tab":{"label":"%s","number":3,"tab_id":"w1:t1"}}}\n' "$TABLABEL" ;;
  *)
    printf '{"error":{"code":"agent_not_found","message":"agent target %s not found"}}\n' "${3:-}" >&2
    exit 1 ;;
esac
STUB
chmod +x "$TMP/bin/herdr"

REPO=$(cd "$TMP/labelled-repo" && pwd -P); export REPO
git -C "$REPO" init -q

pair() {
  OUT=$(cd "$REPO" && env PATH="$TMP/bin:$PATH" TABLABEL="$1" HERDR_ENV=1 HERDR_PANE_ID=w1:p1 HERDR_SKIP_HOOK_CHECK=1 \
    "$PAIR" --dry-run --skip-doctor --builder claude --reviewer claude 2>&1)
}

pair 3
chk "the planner pane is labelled with its kind" 'pane rename w1:p1 planner (claude)'
chk "the builder pane is labelled with its kind" 'builder (claude)'
chk "the reviewer pane is labelled with its kind" 'reviewer (claude)'
chk "a tab that still has its number is named for the repo" 'tab rename w1:t1 tandem: labelled-repo'

pair "my own tab"
chk "a labelled pair still labels its panes" 'builder (claude)'
lack "a tab a person named is left alone" 'tab rename'

printf '\npair-labels: passed %d, failed %d\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

#!/usr/bin/env bash
# test-staleness.sh skips the installed-binary comparison inside a check that
# tandem runs (TANDEM_BUILD_CHECK=1, set by internal/check). The builder cannot
# reinstall tandem, so a build of tandem itself would otherwise never pass.
# tandem-unpair runs the check without the variable, so the comparison still
# guards a sign-off.
set -uo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PASS=0; FAIL=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }

STUB=$(mktemp -d); trap 'rm -rf "$STUB"' EXIT
# A tandem whose hash never matches the source. No tandem-project sits beside
# it, so the suite's stale-script checks skip themselves.
cat > "$STUB/tandem" <<'EOF'
#!/usr/bin/env bash
[ "$1 $2" = "version --hash" ] && { echo stale; exit 0; }
exit 2
EOF
chmod +x "$STUB/tandem"
# The source hash needs go (bin/lib/source-hash.sh), so its folder stays.
GO=$(command -v go) || { echo "go is not on PATH" >&2; exit 1; }
P="$STUB:$(dirname "$GO"):/usr/bin:/bin:/usr/sbin:/sbin"

OUT=$(env -u TANDEM_BUILD_CHECK PATH="$P" bash "$ROOT/tests/test-staleness.sh" "$ROOT" 2>&1); RC=$?
[ "$RC" -ne 0 ] && ok "a person's check fails on a stale tandem" \
  || nope "a person's check fails on a stale tandem (exit $RC)"
case "$OUT" in
  *"installed tandem is STALE"*) ok "and names the remedy" ;;
  *) nope "and names the remedy (got: $OUT)" ;;
esac

OUT=$(TANDEM_BUILD_CHECK=1 PATH="$P" bash "$ROOT/tests/test-staleness.sh" "$ROOT" 2>&1); RC=$?
[ "$RC" -eq 0 ] && ok "a tandem build check passes on a stale tandem" \
  || nope "a tandem build check passes on a stale tandem (exit $RC: $OUT)"
case "$OUT" in
  *"skipping the installed-binary match"*) ok "and says it skipped the match" ;;
  *) nope "and says it skipped the match (got: $OUT)" ;;
esac

printf '\nstaleness-build-check: passed %d, failed %d\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

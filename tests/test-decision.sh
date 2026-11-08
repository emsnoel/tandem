#!/usr/bin/env bash
# test-decision.sh: `tandem decision assess` stays quiet with no credential,
# redacts what it would send, and pins its model.
set -uo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
FAILED=0
nope() { printf '  FAIL %s\n' "$*"; FAILED=$((FAILED+1)); }
good() { printf '  ok   %s\n' "$*"; }

TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
mkdir -p "$TMP/repo"
PLAN="$TMP/plan.md"
printf '# plan\n\nChange the retry count from 3 to 5 in `net/retry.go`.\n' > "$PLAN"

# 5. tandem decision itself is silent and exits 0 with no credential.
export GOCACHE="${GOCACHE_OVERRIDE:-$ROOT/.gocache}"
export GOMODCACHE="${GOMODCACHE_OVERRIDE:-$ROOT/.gocache/mod}"
(cd "$ROOT" && go build -o "$TMP/tandem-real" ./cmd/tandem) \
  || { nope "could not build tandem"; exit 1; }
unset TYPESAFE_API_KEY
OUT=$("$TMP/tandem-real" decision assess --plan "$PLAN" --root "$TMP/repo" --render 2>/dev/null)
RC=$?
if [ "$RC" -ne 0 ]; then
  nope "unconfigured decision assess exited $RC, want 0"
elif [ -n "$OUT" ]; then
  nope "unconfigured decision assess printed output: $OUT"
else
  good "unconfigured assess is silent and exits 0"
fi

# 6. A dry run redacts, and pins the model.
printf '# Plan\n\nexport DEPLOY_TOKEN=abcd1234efgh5678\n' > "$TMP/secret.md"
DRY=$("$TMP/tandem-real" decision assess --plan "$TMP/secret.md" --root "$TMP/repo" --dry-run 2>/dev/null)
if printf '%s' "$DRY" | grep -q 'abcd1234efgh5678'; then
  nope "dry run leaked a secret value"
elif printf '%s' "$DRY" | grep -q 'REDACTED'; then
  good "dry run redacts before showing the payload"
else
  nope "dry run produced no redaction marker"
fi
if printf '%s' "$DRY" | grep -q '"model": *"jev-1.13.0"'; then
  good "dry run pins the model version"
else
  nope "dry run did not pin jev-1.13.0"
fi

# The removed builder judging subcommand must reject even its former flags.
OUT=$("$TMP/tandem-real" decision "judge" --in "$TMP/missing.json" --out - --root "$TMP/repo" 2>&1)
RC=$?
if [ "$RC" -ne 2 ]; then
  nope "removed judging subcommand exited $RC, want usage exit 2"
elif [ "$OUT" != 'usage: tandem decision assess --plan P --root R [--render] [--dry-run]' ]; then
  nope "removed judging subcommand did not show the remaining assess usage: $OUT"
else
  good "removed judging subcommand reports assess usage and exits 2"
fi

exit "$FAILED"

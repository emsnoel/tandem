#!/usr/bin/env bash
# install.sh --from-source must refuse loudly when jq is missing.
set -uo pipefail
ROOT="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
PASS=0; FAIL=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }

# A PATH of symlinks to the real go, bash and shasum, so the result does not
# depend on whether this machine has jq.
STUB=$(mktemp -d); DEST=$(mktemp -d)
trap 'rm -rf "$STUB" "$DEST"' EXIT

missing=0
for tool in go bash dirname; do
  p=$(command -v "$tool" 2>/dev/null) || { missing=1; break; }
  ln -s "$p" "$STUB/$tool"
done
if [ "$missing" -eq 0 ]; then
  if command -v shasum >/dev/null 2>&1; then
    ln -s "$(command -v shasum)" "$STUB/shasum"
  elif command -v sha256sum >/dev/null 2>&1; then
    ln -s "$(command -v sha256sum)" "$STUB/sha256sum"
  else
    missing=1
  fi
fi

# Present so jq is the only thing a release install would miss.
for tool in curl tar; do
  p=$(command -v "$tool" 2>/dev/null) && ln -s "$p" "$STUB/$tool"
done

if [ "$missing" -eq 1 ]; then
  echo "  skip install-jq-check: go, bash, dirname, and shasum/sha256sum must all be on PATH to isolate the jq check"
  exit 0
fi

for mode in "" --from-source; do
  label="${mode:-a release install}"
  # shellcheck disable=SC2086
  OUT=$(PATH="$STUB" "$ROOT/install.sh" $mode "$DEST" 2>&1); RC=$?
  [ "$RC" -ne 0 ] && ok "$label refuses when jq is missing" \
    || nope "$label refuses when jq is missing (exit $RC)"
  case "$OUT" in
    *jq*) ok "and names jq in the FATAL message" ;;
    *)    nope "and names jq in the FATAL message (got: $OUT)" ;;
  esac
  [ -e "$DEST/tandem" ] && nope "and installs no binary" || ok "and installs no binary"
done

printf '\ninstall-jq-check: passed %d, failed %d\n' "$PASS" "$FAIL"; [ "$FAIL" -eq 0 ]

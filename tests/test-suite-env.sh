#!/usr/bin/env bash
# The suite must not inherit the operator's feature switches. Fixtures that
# test a switch set it themselves.
set -uo pipefail
ROOT_DIR=$(cd "$(dirname "$0")/.." && pwd)

PASS=0; FAIL=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }

# The outer environment turns every control on, then the scrub runs.
OUT=$(HERDR_TRIAGE_SKIP=1 HERDR_JUDGE=1 HERDR_DECISION=1 TYPESAFE_API_KEY=ts-x \
      TANDEM_BUILD_ENGINE=durable TANDEM_STATE_HOME=/nonexistent \
      bash -c '. "$1"; printf "%s|%s|%s|%s|%s|%s" "${HERDR_TRIAGE_SKIP-unset}" "${HERDR_JUDGE-unset}" "${HERDR_DECISION-unset}" "${TYPESAFE_API_KEY-unset}" "${TANDEM_BUILD_ENGINE-unset}" "${TANDEM_STATE_HOME-unset}"' _ "$ROOT_DIR/tests/lib/clean-env.sh")
[ "$OUT" = "unset|unset|unset|unset|unset|unset" ] \
  && ok "the scrub clears every feature switch an outer shell can set" \
  || nope "the scrub left some set: $OUT"

# No test may reach the Herdr session it started from. The scrub points Herdr's
# CLI at a socket that does not exist.
OUT=$(HERDR_SOCKET_PATH="$HOME/.config/herdr/herdr.sock" bash -c '. "$1"; printf "%s" "${HERDR_SOCKET_PATH-unset}"' _ "$ROOT_DIR/tests/lib/clean-env.sh")
case "$OUT" in
  ""|unset|"$HOME/.config/herdr/herdr.sock") nope "the scrub leaves Herdr reachable: HERDR_SOCKET_PATH=$OUT" ;;
  *) [ ! -e "$OUT" ] && ok "the scrub points Herdr at a socket that does not exist" || nope "the scrub's Herdr socket exists: $OUT" ;;
esac

# A suite run from a Herdr pane must not inherit the pane's identity, or a
# fixture could pass locally and fail on CI.
OUT=$(HERDR_ENV=1 HERDR_PANE_ID=w1:p1 HERDR_TAB_ID=w1:t1 HERDR_WORKSPACE_ID=w1 \
      bash -c '. "$1"; printf "%s|%s|%s|%s" "${HERDR_ENV-unset}" "${HERDR_PANE_ID-unset}" "${HERDR_TAB_ID-unset}" "${HERDR_WORKSPACE_ID-unset}"' _ "$ROOT_DIR/tests/lib/clean-env.sh")
[ "$OUT" = "unset|unset|unset|unset" ] \
  && ok "the scrub clears the Herdr pane the suite was started from" \
  || nope "the scrub left the Herdr pane's identity set: $OUT"

# run.sh must actually use the scrub, or the file above protects nothing.
grep -q 'lib/clean-env.sh' "$ROOT_DIR/tests/run.sh" \
  && ok "run.sh sources the scrub" || nope "run.sh does not source tests/lib/clean-env.sh"

# The suite itself, started with HERDR_TRIAGE_SKIP=1, sees it unset.
SCRATCH=$(mktemp -d); trap 'rm -rf "$SCRATCH"' EXIT
mkdir -p "$SCRATCH/tests/lib"
cp "$ROOT_DIR/tests/lib/clean-env.sh" "$SCRATCH/tests/lib/"
sed -n '/^\. .*clean-env\.sh/p' "$ROOT_DIR/tests/run.sh" > "$SCRATCH/tests/header.sh"
printf 'printf "%%s" "${HERDR_TRIAGE_SKIP-unset}"\n' >> "$SCRATCH/tests/header.sh"
OUT=$(cd "$SCRATCH/tests" && HERDR_TRIAGE_SKIP=1 bash -c '. ./header.sh' 2>&1)
[ "$OUT" = unset ] && ok "run.sh's own line leaves HERDR_TRIAGE_SKIP unset under an inherited 1" || nope "inherited HERDR_TRIAGE_SKIP survived run.sh's scrub line: '$OUT'"

# A suite that exits non-zero without a FAIL line is still named.
mkdir -p "$SCRATCH/loop/tests"
printf 'exit 3\n' > "$SCRATCH/loop/tests/test-silent.sh"
{ printf 'FAILED=0; WITH_CLIS=0; hdr() { :; }\n'
  sed -n '/^for t in tests\/test-\*\.sh/,/^done/p' "$ROOT_DIR/tests/run.sh"
  printf 'echo "failed=$FAILED"\n'; } > "$SCRATCH/loop/loop.sh"
OUT=$(cd "$SCRATCH/loop" && bash ./loop.sh 2>&1)
case "$OUT" in
  *"  FAIL test-silent (exit 3)"*"failed=1"*) ok "run.sh names a suite that exits non-zero without a FAIL line" ;;
  *) nope "run.sh's loop on a silent failure printed: $OUT" ;;
esac

# The fake event socket reports a server that never starts, and gives up.
OUT=$(TANDEM_FAKE_SOCK_BIN=/usr/bin/true bash -c '. "$1"; fake_socket_start "$2"; echo "rc=$?"' _ "$ROOT_DIR/tests/lib/fake-socket.sh" "$SCRATCH" 2>&1)
[ "$OUT" = "$(printf '  FAIL fake socket did not start\nrc=1')" ] \
  && ok "a fake socket that never starts is a FAIL line" || nope "fake socket start failure printed: $OUT"

printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

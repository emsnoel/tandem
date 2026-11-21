#!/usr/bin/env bash
# The suite must not inherit the operator's feature switches. Fixtures that
# test a switch set it themselves.
set -uo pipefail
ROOT_DIR=$(cd "$(dirname "$0")/.." && pwd)

PASS=0; FAIL=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }

# The outer environment turns every control on, then the scrub runs.
OUT=$(HERDR_DECISION=1 TYPESAFE_API_KEY=ts-x \
      TANDEM_BUILD_ENGINE=durable TANDEM_STATE_HOME=/nonexistent \
      bash -c '. "$1"; printf "%s|%s|%s|%s" "${HERDR_DECISION-unset}" "${TYPESAFE_API_KEY-unset}" "${TANDEM_BUILD_ENGINE-unset}" "${TANDEM_STATE_HOME-unset}"' _ "$ROOT_DIR/tests/lib/clean-env.sh")
[ "$OUT" = "unset|unset|unset|unset" ] \
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

# The suite itself, started with HERDR_DECISION=1, sees it unset.
SCRATCH=$(mktemp -d); trap 'rm -rf "$SCRATCH"' EXIT
mkdir -p "$SCRATCH/tests/lib"
cp "$ROOT_DIR/tests/lib/clean-env.sh" "$SCRATCH/tests/lib/"
sed -n '/^\. .*clean-env\.sh/p' "$ROOT_DIR/tests/run.sh" > "$SCRATCH/tests/header.sh"
printf 'printf "%%s" "${HERDR_DECISION-unset}"\n' >> "$SCRATCH/tests/header.sh"
OUT=$(cd "$SCRATCH/tests" && HERDR_DECISION=1 bash -c '. ./header.sh' 2>&1)
[ "$OUT" = unset ] && ok "run.sh's own line leaves HERDR_DECISION unset under an inherited 1" || nope "inherited HERDR_DECISION survived run.sh's scrub line: '$OUT'"

# run.sh runs its suites through run_suites (tests/lib/run-suites.sh).
grep -q 'lib/run-suites.sh' "$ROOT_DIR/tests/run.sh" && grep -q '^run_suites ' "$ROOT_DIR/tests/run.sh" \
  && ok "run.sh runs its suites through run_suites" || nope "run.sh does not use tests/lib/run-suites.sh"

# suites RUN_ARGS...: run run_suites over the suites in $SCRATCH/loop.
mkdir -p "$SCRATCH/loop"
suites() { bash -c '. "$1"; shift; FAILED=0; hdr() { printf "== %s ==\n" "$1"; }; run_suites "$@"; echo "failed=$FAILED"' \
  _ "$ROOT_DIR/tests/lib/run-suites.sh" "$@" 2>&1; }

# A suite that exits non-zero without a FAIL line is still named.
printf 'exit 3\n' > "$SCRATCH/loop/test-silent.sh"
OUT=$(suites 2 "$SCRATCH/loop/test-silent.sh")
case "$OUT" in
  *"  FAIL test-silent (exit 3)"*"failed=1"*) ok "run.sh names a suite that exits non-zero without a FAIL line" ;;
  *) nope "run_suites on a silent failure printed: $OUT" ;;
esac

# Suites run at once, yet each prints whole and in the order given. The first
# suite is the slowest, so a serial run would take the sum of the sleeps.
printf 'sleep 2; echo a1; echo a2\n' > "$SCRATCH/loop/test-a.sh"
printf 'sleep 1; echo b1; exit 4\n'  > "$SCRATCH/loop/test-b.sh"
printf 'echo c1; sleep 1; echo c2\n' > "$SCRATCH/loop/test-c.sh"
S=$(date +%s)
OUT=$(suites 3 "$SCRATCH/loop/test-a.sh" "$SCRATCH/loop/test-b.sh" "$SCRATCH/loop/test-c.sh")
E=$(( $(date +%s) - S ))
WANT=$(printf '== test-a ==\na1\na2\n== test-b ==\nb1\n  FAIL test-b (exit 4)\n== test-c ==\nc1\nc2\nfailed=1')
[ "$OUT" = "$WANT" ] && ok "run_suites prints each suite whole, in order, and counts failures" \
  || nope "run_suites printed: $OUT"
[ "$E" -lt 4 ] && ok "run_suites runs suites at the same time (${E}s for 4s of sleeps)" \
  || nope "run_suites took ${E}s for 4s of sleeps, so it ran them one at a time"

# --jobs 1 runs them one at a time.
S=$(date +%s)
suites 1 "$SCRATCH/loop/test-a.sh" "$SCRATCH/loop/test-c.sh" >/dev/null
E=$(( $(date +%s) - S ))
[ "$E" -ge 3 ] && ok "run_suites with one job runs suites one at a time" \
  || nope "run_suites with one job took ${E}s for 3s of sleeps"

# The fake event socket reports a server that never starts, and gives up.
OUT=$(TANDEM_FAKE_SOCK_BIN=/usr/bin/true bash -c '. "$1"; fake_socket_start "$2"; echo "rc=$?"' _ "$ROOT_DIR/tests/lib/fake-socket.sh" "$SCRATCH" 2>&1)
[ "$OUT" = "$(printf '  FAIL fake socket did not start\nrc=1')" ] \
  && ok "a fake socket that never starts is a FAIL line" || nope "fake socket start failure printed: $OUT"

printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

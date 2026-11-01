#!/usr/bin/env bash
# tandem-unpair --clean-trust removes the trust entries for the builder's and
# the reviewer's kinds. The reviewer is closed by the same unpair, so it does
# not count as another agent still working.
#
# Hermetic: herdr and parts of tandem are stubbed, and jq fails if called.
set -uo pipefail
. "$(cd "$(dirname "$0")" && pwd)/lib/records.sh"
UNPAIR="${1:-$(cd "$(dirname "$0")/.." && pwd)/bin/tandem-unpair}"
[ -x "$UNPAIR" ] || { echo "not executable: $UNPAIR" >&2; exit 2; }

PASS=0; FAIL=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }
chk()  { case "$OUT" in *"$2"*) ok "$1" ;; *) nope "$1 (missing: $2)" ;; esac; }
nchk() { case "$OUT" in *"$2"*) nope "$1 (unexpected: $2)" ;; *) ok "$1" ;; esac; }

command -v jq >/dev/null || { echo "need jq" >&2; exit 2; }

TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
mkdir -p "$TMP/bin" "$TMP/repo"
# A stub jq that fails: tandem-unpair asks tandem, so any jq call is a bug.
printf '#!/bin/sh\necho "jq was called: $*" >&2\nexit 99\n' > "$TMP/bin/jq"
git -C "$TMP/repo" init -q .
mkdir -p "$TMP/repo/.tandem-relay"
write_reviewer "$TMP/repo/.tandem-relay" codex
REPO_PHYS=$(cd "$TMP/repo" && pwd -P)

# Agents: planner (claude, %1), builder (%2) and reviewer (%9), all in the
# repo. FAKE_BUILDER_KIND, FAKE_REVIEWER_KIND, EXTRA and EXTRA_CWD are set per
# case, under names tandem-unpair does not use itself.
# HERDR_FAIL makes `agent list` fail. The real binary answers `reviewer show`,
# `agent count`, `agent kind` and `config`, reading this herdr.
cat > "$TMP/bin/herdr" <<'STUB'
#!/usr/bin/env bash
if [[ "$1 $2" == "agent list" ]]; then
  [[ -n "${HERDR_FAIL:-}" ]] && { echo '{"error":{"code":"timeout","message":"no answer"}}' >&2; exit 1; }
  a() { printf '{"pane_id":"%s","agent":"%s","name":%s,"agent_status":"idle","cwd":"%s"}' "$@"; }
  printf '{"result":{"agents":[%s,%s,%s' "$(a %1 claude '"planner"' "$REPO_PHYS")" \
    "$(a %2 "$FAKE_BUILDER_KIND" '"builder"' "$REPO_PHYS")" "$(a %9 "$FAKE_REVIEWER_KIND" '"reviewer"' "$REPO_PHYS")"
  [[ -n "$EXTRA" ]] && printf ',%s' "$(a %7 "$EXTRA" null "${EXTRA_CWD:-$REPO_PHYS}")"
  printf ']}}\n'
  exit 0
fi
exit 0
STUB
export REAL_TANDEM; REAL_TANDEM=$(command -v tandem) || { echo "need an installed tandem" >&2; exit 2; }
cat > "$TMP/bin/tandem" <<'STUB'
#!/usr/bin/env bash
case "$1 $2" in
  "reviewer show"|"agent count"|"agent kind"|"config resolve") exec "$REAL_TANDEM" "$@" ;;
  "agent snapshot")
    case "$3" in
      reviewer) printf '%%9\t%s\tidle\n' "$REPO_PHYS"; exit 0 ;;
      *)        printf '%%2\t%s\tidle\n' "$REPO_PHYS"; exit 0 ;;
    esac ;;
  "trust clean") printf '%s\n' "$*" >> "$TRUST_LOG"; printf 'removed\n'; exit 0 ;;
  "pane close") exit 0 ;;
  "build archive") exit 0 ;;
esac
case "$1" in
  paths) shift; while (( $# )); do [[ "$1" == --root ]] && { printf '%s/.tandem-relay\n' "$2"; exit 0; }; shift; done; exit 1 ;;
  pane) exit 0 ;;
esac
exit 0
STUB
chmod +x "$TMP/bin"/*

# run <builder kind> <reviewer kind> <extra kind or "">: sets OUT and the
# trust log. EXTRA_CWD and HERDR_FAIL are read from the caller.
EXTRA_CWD=""; HERDR_FAIL=""
run() {
  : > "$TMP/trust.log"
  OUT=$(cd "$TMP/repo" && PATH="$TMP/bin:$PATH" HERDR_ENV=1 REPO_PHYS="$REPO_PHYS" \
          FAKE_BUILDER_KIND="$1" FAKE_REVIEWER_KIND="$2" EXTRA="$3" TRUST_LOG="$TMP/trust.log" \
          EXTRA_CWD="$EXTRA_CWD" HERDR_FAIL="$HERDR_FAIL" \
          "$UNPAIR" --force --skip-checks --keep-relay --clean-trust 2>&1) || true
}
cleaned() { grep -c -- "--kind $1 " "$TMP/trust.log"; }

echo "--- a copilot builder and a codex reviewer: both kinds are cleaned ---"
run copilot codex ""
[ "$(cleaned copilot)" = 1 ] && ok "the builder's kind is cleaned" || nope "builder kind cleaned $(cleaned copilot) times"
[ "$(cleaned codex)" = 1 ]   && ok "the reviewer's kind is cleaned" || nope "reviewer kind cleaned $(cleaned codex) times"

echo "--- a codex builder and a codex reviewer: the pair's own reviewer does not block ---"
run codex codex ""
[ "$(cleaned codex)" = 1 ] && ok "codex is cleaned once" || nope "codex cleaned $(cleaned codex) times"
nchk "the reviewer is not counted as another agent" "still working"

echo "--- a third codex agent in the repo still blocks the clean ---"
run codex codex codex
[ "$(cleaned codex)" = 0 ] && ok "a third agent keeps the trust entry" || nope "trust was cleaned under a third agent"
chk "it says why" "1 other codex agent(s) still working"

echo "--- a codex agent in a sibling directory does not block the clean ---"
EXTRA_CWD="$REPO_PHYS-other"
run codex codex codex
EXTRA_CWD=""
[ "$(cleaned codex)" = 1 ] && ok "an agent in $REPO_PHYS-other is not in the repo" || nope "codex cleaned $(cleaned codex) times"

echo "--- herdr fails: nothing is cleaned ---"
HERDR_FAIL=1
run codex codex ""
HERDR_FAIL=""
[ ! -s "$TMP/trust.log" ] && ok "no trust entry is removed when herdr does not answer" || nope "trust clean ran: $(cat "$TMP/trust.log")"
chk "it says it could not tell" "could not determine"

nchk "no jq call" "jq was called"

echo "clean-trust: passed $PASS, failed $FAIL"
[ "$FAIL" = 0 ]

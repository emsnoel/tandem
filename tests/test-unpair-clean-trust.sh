#!/usr/bin/env bash
# tandem-unpair --clean-trust removes the trust entries for the builder's and
# the reviewer's kinds. The reviewer is closed by the same unpair, so it does
# not count as another agent still working.
#
# Hermetic: tandem and herdr are stubbed.
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
ln -s "$(command -v jq)" "$TMP/bin/jq"
printf '#!/usr/bin/env bash\nexit 0\n' > "$TMP/bin/herdr"
git -C "$TMP/repo" init -q .
mkdir -p "$TMP/repo/.tandem-relay"
write_reviewer "$TMP/repo/.tandem-relay" codex
REPO_PHYS=$(cd "$TMP/repo" && pwd -P)

# Agents: planner (claude, %1), builder (%2) and reviewer (%9), all in the
# repo. BUILDER_KIND, REVIEWER_KIND and EXTRA are set per case. The real
# binary answers `reviewer show`.
export REAL_TANDEM; REAL_TANDEM=$(command -v tandem) || { echo "need an installed tandem" >&2; exit 2; }
cat > "$TMP/bin/tandem" <<'STUB'
#!/usr/bin/env bash
case "$1 $2" in
  "reviewer show") exec "$REAL_TANDEM" "$@" ;;
  "agent list")
    jq -n --arg cwd "$REPO_PHYS" --arg b "$BUILDER_KIND" --arg r "$REVIEWER_KIND" --arg x "$EXTRA" '
      {result:{agents:([
        {pane_id:"%1",agent:"claude",name:"planner",cwd:$cwd},
        {pane_id:"%2",agent:$b,name:"builder",cwd:$cwd},
        {pane_id:"%9",agent:$r,name:"reviewer",cwd:$cwd}]
        + (if $x == "" then [] else [{pane_id:"%7",agent:$x,name:null,cwd:$cwd}] end))}}'
    exit 0 ;;
  "agent snapshot")
    case "$3" in
      reviewer) printf '%%9\t%s\tidle\n' "$REPO_PHYS"; exit 0 ;;
      *)        printf '%%2\t%s\tidle\n' "$REPO_PHYS"; exit 0 ;;
    esac ;;
  "trust clean") printf '%s\n' "$*" >> "$TRUST_LOG"; printf '{"removed":true}\n'; exit 0 ;;
  "pane close") exit 0 ;;
  "build archive") exit 0 ;;
esac
case "$1" in
  paths) shift; while (( $# )); do [[ "$1" == --root ]] && { printf '%s/.tandem-relay\n' "$2"; exit 0; }; shift; done; exit 1 ;;
  pane) exit 0 ;;
esac
exit 0
STUB
cat > "$TMP/bin/tandem-project" <<'STUB'
#!/usr/bin/env bash
printf '{"root":"%s","check_command":"true"}\n' "$2"
STUB
chmod +x "$TMP/bin"/*

# run <builder kind> <reviewer kind> <extra kind or "">: sets OUT and the
# trust log.
run() {
  : > "$TMP/trust.log"
  OUT=$(cd "$TMP/repo" && PATH="$TMP/bin:$PATH" HERDR_ENV=1 REPO_PHYS="$REPO_PHYS" \
          BUILDER_KIND="$1" REVIEWER_KIND="$2" EXTRA="$3" TRUST_LOG="$TMP/trust.log" \
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

echo "clean-trust: passed $PASS, failed $FAIL"
[ "$FAIL" = 0 ]

#!/usr/bin/env bash
# tandem-unpair must remove every mailbox the pair created.
#
# When the builder works in a subdirectory, the pair writes to two places:
#   builder.json, reviews/   the builder pane's own cwd (unwalked)
#   q/, a/, bridge.pid       the git toplevel (walked)
# A stale review left behind could pass the gate for the next pair.
#
# Hermetic: tandem and herdr are stubbed, and jq fails if called.
set -uo pipefail
. "$(cd "$(dirname "$0")" && pwd)/lib/records.sh"
UNPAIR="${1:-$(cd "$(dirname "$0")/.." && pwd)/bin/tandem-unpair}"
[ -x "$UNPAIR" ] || { echo "not executable: $UNPAIR" >&2; exit 2; }

PASS=0; FAIL=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }
chk()  { case "$OUT" in *"$2"*) ok "$1" ;; *) nope "$1 (missing: $2)" ;; esac; }

command -v jq >/dev/null || { echo "need jq" >&2; exit 2; }

TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
mkdir -p "$TMP/bin"
# A stub jq that fails: tandem-unpair asks tandem, so any jq call is a bug.
printf '#!/bin/sh\necho "jq was called: $*" >&2\nexit 99\n' > "$TMP/bin/jq"
printf '#!/usr/bin/env bash\nexit 0\n' > "$TMP/bin/herdr"
# `paths relay` answers from its --root flag, so the test sees the script's own
# choice of root. The real binary answers `reviewer show`.
export REAL_TANDEM; REAL_TANDEM=$(command -v tandem) || { echo "need an installed tandem" >&2; exit 2; }
cat > "$TMP/bin/tandem" <<'STUB'
#!/usr/bin/env bash
case "$1 $2" in
  "reviewer show") exec "$REAL_TANDEM" "$@" ;;
  # `build archive` is what tandem-unpair must run before it deletes a relay.
  # ARCHIVE_FAIL models a copy that could not be verified.
  "build archive") printf '%s\n' "$*" >> "$ARCHIVE_LOG"; [[ -n "${ARCHIVE_FAIL:-}" ]] && exit 1; exit 0 ;;
  # DEAD models a builder pane that is gone (rc 10, no cwd). The reviewer pane
  # answers only to its own name, when FAKE_REVIEWER_STATUS says it runs.
  "agent snapshot")
    if [[ "$3" == "${REVIEWER_AGENT:-reviewer}" ]]; then
      [[ -n "${FAKE_REVIEWER_STATUS:-}" ]] || exit 10
      printf '%%9\t%s\t%s\n' "$BCWD" "$FAKE_REVIEWER_STATUS"; exit 0
    fi
    [[ -n "${DEAD:-}" ]] && exit 10; printf '%%2\t%s\tidle\n' "$BCWD"; exit 0 ;;
  "pane close") printf '%s\n' "$3" >> "$PANE_LOG"; exit 0 ;;
  "config check-command") printf 'true\0'; exit 0 ;;
esac
case "$1" in
  paths)
    shift
    while (( $# )); do
      [[ "$1" == --root ]] && { printf '%s/.tandem-relay\n' "$2"; exit 0; }
      shift
    done
    exit 1 ;;
  pane) exit 0 ;;
esac
exit 0
STUB
chmod +x "$TMP/bin"/*

# fixture <dir>: a relay mailbox at the toplevel and an identity mailbox under
# sub/, as a pair made from sub/ leaves them.
fixture() {
  rm -rf "$1"; mkdir -p "$1/sub"
  git -C "$1" init -q .
  mkdir -p "$1/.tandem-relay/q" "$1/sub/.tandem-relay/reviews"
  printf '{"kind":"codex","autonomy":"auto","adopted":false}\n' \
    > "$1/sub/.tandem-relay/builder.json"
  printf '{"plan":"p","verdict":"clean"}\n' \
    > "$1/sub/.tandem-relay/reviews/20260910-000001-1-1.json"
  # tandem-pair records the builder's cwd so teardown works after the pane is
  # gone.
  printf '%s\n' "$1/sub" > "$1/.tandem-relay/builder-cwd"
}

# Set DEAD on its own line: bash 3.2 does not reliably scope a prefix
# assignment to a function call.
DEAD=""
run() { # run <builder-cwd> <extra-args...>
  local bcwd="$1"; shift
  # With a dead pane, tandem-unpair falls back to $PWD.
  OUT=$(cd "$REPO" && PATH="$TMP/bin:$PATH" HERDR_ENV=1 BCWD="$bcwd" \
          DEAD="$DEAD" ARCHIVE_LOG="$TMP/archive.log" ARCHIVE_FAIL="${ARCHIVE_FAIL:-}" \
          PANE_LOG="$TMP/panes.log" FAKE_REVIEWER_STATUS="$FAKE_REVIEWER_STATUS" "$UNPAIR" "$@" 2>&1); RC=$?
}
FAKE_REVIEWER_STATUS=""

# --- a subdirectory pair names BOTH mailboxes --------------------------------
REPO="$TMP/repo"
fixture "$REPO"
REPO_PHYS=$(cd "$REPO" && pwd -P)
run "$REPO/sub" --dry-run
chk "the dry run names the relay mailbox"    "$REPO_PHYS/.tandem-relay"
chk "the dry run names the identity mailbox" "$REPO_PHYS/sub/.tandem-relay"

# --- and removes both for real ------------------------------------------------
fixture "$REPO"
run "$REPO/sub"
if [ -d "$REPO/.tandem-relay" ]; then
  nope "the relay mailbox was removed"
else
  ok "the relay mailbox was removed"
fi
# The identity mailbox must be removed too.
if [ -d "$REPO/sub/.tandem-relay" ]; then
  nope "the identity mailbox was removed (builder.json and reviews/ left behind)"
else
  ok "the identity mailbox was removed"
fi

# --- --keep-relay keeps both ---
fixture "$REPO"
run "$REPO/sub" --keep-relay
# Both directories also survive when only one was resolved, so check that
# teardown names the identity mailbox it kept.
chk "--keep-relay names the relay mailbox it kept"    "kept $REPO_PHYS/.tandem-relay"
chk "--keep-relay names the identity mailbox it kept" "kept $REPO_PHYS/sub/.tandem-relay"
if [ -d "$REPO/.tandem-relay" ] && [ -d "$REPO/sub/.tandem-relay" ]; then
  ok "--keep-relay leaves both mailboxes on disk"
else
  nope "--keep-relay removed a mailbox it was told to keep"
fi

# --- a root pair has one mailbox and names it once ---
fixture "$REPO"
run "$REPO" --dry-run
_count=$(printf '%s\n' "$OUT" | grep -c 'rm -rf')
case "$_count" in
  1) ok "a root pair names one mailbox exactly once" ;;
  *) nope "a root pair named $_count mailboxes, want 1" ;;
esac

fixture "$REPO"
run "$REPO"
if [ -d "$REPO/.tandem-relay" ]; then
  nope "a root pair removed its mailbox"
else
  ok "a root pair removed its mailbox"
fi

# --- the builder pane is already gone ---
# $ROOT falls back to $PWD, which at the repo root matches the relay mailbox,
# so the recorded builder cwd must be used to find the subdirectory one.
fixture "$REPO"
DEAD=1
run "$REPO" --dry-run
chk "a dead pane still names the identity mailbox" "$REPO_PHYS/sub/.tandem-relay"

fixture "$REPO"
run "$REPO"
if [ -d "$REPO/sub/.tandem-relay" ]; then
  nope "a dead pane left the identity mailbox behind"
else
  ok "a dead pane still removes the identity mailbox"
fi

# --- a dead pane with no recorded cwd ---
# Nothing can find the identity mailbox, so teardown must say so.
fixture "$REPO"
rm -f "$REPO/.tandem-relay/builder-cwd"
run "$REPO"
chk "an unrecoverable cwd is reported, not passed over" "WARNING the builder pane is gone"
DEAD=""

# --- a legacy build is archived before its relay is deleted ---
# The relay holds the only copy of the build record and its reports.
with_build() { mkdir -p "$1/.tandem-relay/build" && printf '{"id":"b1"}\n' > "$1/.tandem-relay/build/build.json"; }

fixture "$REPO"; with_build "$REPO"; rm -f "$TMP/archive.log"
run "$REPO/sub" --dry-run
chk "the dry run says the build would be archived" "tandem build archive --root"
[ -f "$TMP/archive.log" ] && nope "a dry run archived" || ok "a dry run archives nothing"

fixture "$REPO"; with_build "$REPO"; rm -f "$TMP/archive.log"
run "$REPO/sub"
grep -q -- "build archive --root $REPO_PHYS" "$TMP/archive.log" 2>/dev/null \
  && ok "teardown archives the build, naming the repository root" || nope "teardown did not archive the build"
[ -d "$REPO/.tandem-relay" ] && nope "the relay survived a verified archive" || ok "the relay is removed once the archive succeeds"

# A failed archive must leave everything in place.
fixture "$REPO"; with_build "$REPO"; rm -f "$TMP/archive.log"
ARCHIVE_FAIL=1
run "$REPO/sub"
chk "a failed archive is reported" "archive"
if [ -f "$REPO/.tandem-relay/build/build.json" ] && [ -d "$REPO/sub/.tandem-relay" ]; then
  ok "a failed archive leaves both mailboxes in place"
else
  nope "a failed archive still deleted evidence"
fi
ARCHIVE_FAIL=""

fixture "$REPO"; with_build "$REPO"; rm -f "$TMP/archive.log"
run "$REPO/sub" --keep-relay
[ -f "$TMP/archive.log" ] && nope "--keep-relay archived a relay it kept" || ok "--keep-relay does not archive"

fixture "$REPO"; rm -f "$TMP/archive.log"
run "$REPO/sub"
[ -f "$TMP/archive.log" ] && nope "a pair with no build was archived" || ok "a pair with no build asks for no archive"

# --- a reviewer pane closes with the builder ---
# Only the recorded reviewer pane is closed.
with_reviewer() { write_reviewer "$1/sub/.tandem-relay"; }

fixture "$REPO"; rm -f "$TMP/panes.log"
FAKE_REVIEWER_STATUS=idle
run "$REPO/sub"
grep -qx '%2' "$TMP/panes.log" 2>/dev/null && ! grep -qx '%9' "$TMP/panes.log" \
  && ok "a pair with no recorded reviewer closes only the builder" || nope "panes closed: $(cat "$TMP/panes.log" 2>/dev/null)"

fixture "$REPO"; with_reviewer "$REPO"; rm -f "$TMP/panes.log"
run "$REPO/sub" --dry-run
chk "the dry run names the reviewer pane" "herdr pane close %9"
[ -f "$TMP/panes.log" ] && nope "a dry run closed a pane" || ok "a dry run closes no pane"

fixture "$REPO"; with_reviewer "$REPO"; rm -f "$TMP/panes.log"
run "$REPO/sub"
grep -qx '%9' "$TMP/panes.log" 2>/dev/null && ok "the recorded reviewer pane is closed" || nope "the reviewer pane was left open ($OUT)"
grep -qx '%2' "$TMP/panes.log" 2>/dev/null && ok "and so is the builder" || nope "the builder pane was left open"
chk "teardown says it closed the reviewer" "closed the reviewer pane %9"

fixture "$REPO"; with_reviewer "$REPO"; rm -f "$TMP/panes.log"
FAKE_REVIEWER_STATUS=working
run "$REPO/sub"
[ "$RC" -eq 9 ] && ok "a reviewer still working refuses with exit 9" || nope "a working reviewer: rc $RC"
chk "and says its review would be lost" "reviewer is still working"
[ -f "$TMP/panes.log" ] && nope "a refused teardown closed a pane" || ok "and nothing is closed"
[ -d "$REPO/.tandem-relay" ] && ok "and the relay is kept" || nope "a refused teardown removed the relay"
run "$REPO/sub" --force
grep -qx '%9' "$TMP/panes.log" 2>/dev/null && ok "--force closes a working reviewer" || nope "--force left the reviewer open"

fixture "$REPO"; with_reviewer "$REPO"; rm -f "$TMP/panes.log"
FAKE_REVIEWER_STATUS=""
run "$REPO/sub"
[ "$RC" -eq 0 ] && grep -qx '%2' "$TMP/panes.log" && ! grep -qx '%9' "$TMP/panes.log" \
  && ok "a reviewer pane already gone is stepped over" || nope "a gone reviewer: rc $RC $OUT"
FAKE_REVIEWER_STATUS=""

# --- a non-git directory ---
# The walk falls back to the cwd, so the two mailboxes coincide.
NOGIT="$TMP/nogit"
rm -rf "$NOGIT"; mkdir -p "$NOGIT/.tandem-relay/q"
run "$NOGIT" --dry-run
_count=$(printf '%s\n' "$OUT" | grep -c 'rm -rf')
case "$_count" in
  1) ok "a non-git pair names one mailbox exactly once" ;;
  *) nope "a non-git pair named $_count mailboxes, want 1" ;;
esac

printf '\nunpair-mailbox: passed %d, failed %d\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

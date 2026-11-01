# Shared setup for the tandem-verdict tests: the stubs, the counters and run.
# Source it from a test in tests/; $0 and $1 are the test's own.
#
# Hermetic: tandem and herdr are stubbed. `tandem agent self --expect` is the
# real command, run against the stubbed herdr, because it decides whether a
# shell is the reviewer pane. jq fails: tandem-verdict writes through tandem,
# so any jq call is a bug. The tests read what it wrote with the real jq.
# shellcheck shell=bash
. "$(cd "$(dirname "$0")" && pwd)/lib/records.sh"
VERDICT_BIN="${1:-$(cd "$(dirname "$0")/.." && pwd)/bin/tandem-verdict}"
[ -x "$VERDICT_BIN" ] || { echo "not executable: $VERDICT_BIN" >&2; exit 2; }

PASS=0; FAIL=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }
chk()  { case "$OUT" in *"$2"*) ok "$1" ;; *) nope "$1 (missing: $2)" ;; esac; }

command -v jq >/dev/null || { echo "need jq" >&2; exit 2; }

TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
mkdir -p "$TMP/bin" "$TMP/repo" "$TMP/mailbox"
rm -f "$TMP/bin/jq"
printf '#!/bin/sh\necho "jq was called: $*" >&2\nexit 99\n' > "$TMP/bin/jq"
(cd "$(dirname "$0")/.." && go build -o "$TMP/tandem-real" ./cmd/tandem) || { echo "could not build tandem" >&2; exit 2; }
export TANDEM_REAL="$TMP/tandem-real"
# Cases about a pane set HERDR_PANE_ID themselves.
unset HERDR_PANE_ID

PLAN="$TMP/repo/p.md"; printf '# plan\n' > "$PLAN"
REVIEW="$TMP/review.md"; printf 'looks clean\n' > "$REVIEW"

# The stub logs every tandem call and answers the reads tandem-verdict makes.
# AGENT_SNAPSHOT_RC, AGENT_PROMPT_RC and AGENT_STATUS choose the branch;
# defaults are found, delivered, idle-then-working.
cat > "$TMP/bin/tandem" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$MARKER"
case "$1 $2" in
  "agent self"|"reviewer show"|"config builder"|"relay write")
    exec "$TANDEM_REAL" "$@" ;;
  "gate record")
    [[ "${GATE_RECORD_RC:-0}" == 0 ]] || exit "$GATE_RECORD_RC"
    exec "$TANDEM_REAL" "$@" ;;
  "config project")
    # Field 1 is the root, echoed from --root; 14 is an invalid manifest.
    root=""; shift 2
    while (( $# )); do [[ "$1" == --root ]] && root="$2"; shift; done
    printf '%s\0' "$root"
    for _ in 2 3 4 5 6 7 8 9 10 11 12 13; do printf '\0'; done
    printf '%s\0' "${INVALID_MANIFEST:-}"
    exit 0 ;;
  "agent snapshot")
    [[ "${AGENT_SNAPSHOT_RC:-0}" == 0 ]] || exit "${AGENT_SNAPSHOT_RC}"
    printf 'w7:p1\t%s\t%s\n' "$REPO" "${SNAPSHOT_STATUS:-idle}"
    exit 0 ;;
  "agent status")
    printf '%s\n' "${AGENT_STATUS_SEQ:-working}"
    exit 0 ;;
  "agent prompt")
    if [[ "${AGENT_PROMPT_RC:-0}" != 0 ]]; then
      printf 'send-failed\tsimulated prompt failure\n' >&2
      exit "${AGENT_PROMPT_RC}"
    fi
    case "${CONFIRM_RC:-0}" in
      25) printf 'agent_never_started\tno turn began\n' >&2; exit 25 ;;
      26) printf 'agent_turn_failed\tturn ended at once\n%s\n' "${PANE_TEXT:-}" >&2; exit 26 ;;
    esac
    exit 0 ;;
esac
case "$1" in
  paths) printf '%s\n' "$MAILBOX"; exit 0 ;;
esac
exit 0
STUB
# HERDR_FAKE makes herdr hang (timeout), fail as the Codex sandbox does
# (denied), or return an error (error). Otherwise w1:p1 is the builder and
# w1:p2 the reviewer.
cat > "$TMP/bin/herdr" <<'STUB'
#!/usr/bin/env bash
case "${HERDR_FAKE:-}" in
  timeout) sleep 30; exit 0 ;;
  denied)  echo 'Error: Os { code: 1, kind: PermissionDenied, message: "Operation not permitted" }' >&2; exit 1 ;;
  error)   echo '{"error":{"code":"internal","message":"herdr failed"}}' >&2; exit 1 ;;
esac
[[ "$1 $2" == "agent list" ]] && { printf '{"result":{"agents":[{"pane_id":"w1:p1","name":"builder","agent_status":"idle"},{"pane_id":"w1:p2","name":"reviewer","agent_status":"idle"}]}}\n'; exit 0; }
exit 0
STUB
chmod +x "$TMP/bin"/*

MARKER="$TMP/calls"
# Cases run as the reviewer pane unless they set HERDR_ROLE themselves.
export HERDR_ROLE=reviewer
run() { : > "$MARKER"; OUT=$(PATH="$TMP/bin:$PATH" MAILBOX="$TMP/mailbox" REPO="$TMP/repo" MARKER="$MARKER" "$@" 2>&1); RC=$?; }

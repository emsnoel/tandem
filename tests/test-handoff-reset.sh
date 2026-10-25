#!/usr/bin/env bash
# The build starts in an empty builder conversation, unless that conversation
# already holds a build.
#
# The reset is sent before the launch prompt, the recorded session is the one
# the build runs in, and a reset that cannot happen never stops a build.
set -uo pipefail
HANDOFF="${1:-$(cd "$(dirname "$0")/.." && pwd)/bin/tandem-handoff}"
[ -x "$HANDOFF" ] || { echo "not executable: $HANDOFF" >&2; exit 2; }

PASS=0; FAIL=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }
has()  { case "$OUT" in *"$2"*) ok "$1" ;; *) nope "$1 (missing: $2)" ;; esac; }
hasno() { case "$OUT" in *"$2"*) nope "$1 (present: $2)" ;; *) ok "$1" ;; esac; }

TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
. "$(cd "$(dirname "$0")" && pwd)/lib/stub-handoff.sh"
stub_handoff_env "$TMP"
REPO="$TMP/repo"; MB="$REPO/.tandem-relay"
mkdir -p "$REPO/docs/plans" "$MB"
PLAN="$REPO/docs/plans/p.md"; printf '# plan\n' > "$PLAN"
CALLS="$TMP/calls"; SENT="$TMP/sent"

# Every reset and prompt is logged in order. STUB_RESET is what `agent reset`
# prints ("EMPTY" prints nothing). `agent session` answers STUB_BEFORE, or
# STUB_AFTER with --not.
cat > "$TMP/bin/tandem.extra" <<'EXTRA'
# STUB_IDLE: the builder never leaves idle, as when a dialog swallows a prompt.
[ -n "${STUB_IDLE:-}" ] && [ "$1 $2" = "agent status" ] && { printf 'idle\n'; exit 0; }
case "$1 $2" in
  "agent reset")
    printf 'reset %s\n' "$*" >> "$CALLS"
    [ "${STUB_RESET:-reset}" = EMPTY ] || printf '%s\n' "${STUB_RESET:-reset}"
    exit 0 ;;
  "agent session")
    case " $* " in
      *" --not "*) printf 'session %s\n' "$*" >> "$CALLS"; v="${STUB_AFTER-s2}" ;;
      *) v="${STUB_BEFORE-s1}" ;;
    esac
    [ -z "$v" ] || printf '%s\n' "$v"
    exit 0 ;;
  "agent explain-reset")
    [ -n "${STUB_NO_EXPLAIN:-}" ] && exit 2
    printf 'SAY %s\n' "$*"; exit 0 ;;
  "agent prompt")
    printf 'prompt\n' >> "$CALLS"; printf '%s' "$4" > "$SENT"
    # --as answers from the status after the prompt; still settled means the
    # prompt never started.
    case " $* " in *" --as "*)
      st=${STUB_STATUS:-working}; [ -n "${STUB_IDLE:-}" ] && st=idle
      case "$st" in idle|done) printf 'agent_never_started\tthe prompt never started\n' >&2; exit 25 ;; esac
      printf 'started\t%s\n' "$st" ;;
    esac
    exit 0 ;;
  "agent status")  printf '%s\n' "${STUB_STATUS:-working}"; exit 0 ;;
esac
EXTRA

run() {  # run [VAR=value...] -- one real (not dry-run) handoff
  rm -f "$CALLS" "$SENT" "$MB/build-session"
  OUT=$(env -u HERDR_DECISION -u HERDR_JUDGE PATH="$TMP/bin:$PATH" HERDR_ENV=1 REPO="$REPO" CALLS="$CALLS" SENT="$SENT" "$@" \
        "$HANDOFF" "$PLAN" 2>&1); RC=$?
}
first_call() { sed -n 1p "$CALLS" 2>/dev/null | cut -d' ' -f1; }
recorded() { cat "$MB/build-session" 2>/dev/null; }

printf '== a reset goes before the build ==\n'
run STUB_RESET=reset
[ "$RC" -eq 0 ] && ok "handoff succeeds" || nope "handoff succeeds (rc $RC)"
[ "$(first_call)" = reset ] && ok "reset is sent first" || nope "reset is sent first ($(tr '\n' '|' < "$CALLS"))"
grep -qx prompt "$CALLS" && ok "then the launch prompt" || nope "then the launch prompt"
grep -q -- "--root $REPO" "$CALLS" && ok "reset is told the project root" || nope "reset is told the project root"
grep -q -- "--not s1" "$CALLS" && ok "asks for the session that is not the pre-reset one" \
  || nope "asks for the session that is not the pre-reset one"
[ "$(recorded)" = s2 ] && ok "records the session the build runs in" || nope "records the session the build runs in (got '$(recorded)')"
has "explains the reset through tandem, for the build" "SAY agent explain-reset reset --for build"
hasno "a confirmed reset does not warn" "WARNING"

printf '\n== codex: the id moves only once the build starts ==\n'
run STUB_RESET=sent STUB_AFTER=s2
[ "$RC" -eq 0 ] && grep -qx prompt "$CALLS" && ok "sent: delivers" || nope "sent: delivers (rc $RC)"
[ "$(recorded)" = s2 ] && ok "sent: records the new session" || nope "sent: records the new session (got '$(recorded)')"
hasno "sent, then moved: no warning" "WARNING"

run STUB_RESET=sent STUB_AFTER=s1
[ "$RC" -eq 0 ] && grep -qx prompt "$CALLS" && ok "sent, never moved: still delivers" || nope "sent, never moved: still delivers"
has "sent, never moved: warns the build may carry the review" "may be carrying"
# An unmoved id is still the review's session, so recording it would make the
# next handoff skip a needed reset.
[ -e "$MB/build-session" ] && nope "sent, never moved: records nothing (got '$(recorded)')" \
  || ok "sent, never moved: records nothing"

printf '\n== a reset that cannot happen never blocks the build ==\n'
for tok in skip:unsupported skip:not-idle skip:no-session skip:no-identity EMPTY; do
  run STUB_RESET="$tok"
  if [ "$RC" -eq 0 ] && grep -qx prompt "$CALLS"; then ok "$tok: still delivers"; else nope "$tok: still delivers (rc $RC)"; fi
  has "$tok: explains it through tandem" "SAY agent explain-reset"
done

run STUB_RESET=skip:holds-build STUB_AFTER=s1
[ "$RC" -eq 0 ] && grep -qx prompt "$CALLS" && ok "holds-build: delivers into the existing conversation" \
  || nope "holds-build: delivers into the existing conversation"
hasno "holds-build: is expected, not a warning" "WARNING"

printf '\n== no session reported means nothing recorded ==\n'
run STUB_RESET=skip:no-session STUB_BEFORE= STUB_AFTER=
[ -e "$MB/build-session" ] && nope "writes no empty build-session" || ok "writes no empty build-session"

printf '\n== dry run ==\n'
rm -f "$CALLS" "$SENT" "$MB/build-session"
OUT=$(env PATH="$TMP/bin:$PATH" HERDR_ENV=1 REPO="$REPO" CALLS="$CALLS" STUB_RESET=would-reset \
      "$HANDOFF" "$PLAN" --dry-run 2>&1)
grep -q -- "--dry-run" "$CALLS" && ok "asks reset for a dry run" || nope "asks reset for a dry run"
grep -qx prompt "$CALLS" && nope "sends no prompt" || ok "sends no prompt"
[ -e "$MB/build-session" ] && nope "writes no build-session" || ok "writes no build-session"
has "reports what the reset would do" "would-reset"

printf '\n== a swallowed prompt (exit 7) records nothing ==\n'
# The stub answers --as at once with the real command's exit code.
run STUB_RESET=reset STUB_IDLE=1
[ "$RC" -eq 7 ] && ok "exits 7" || nope "exits 7 (rc $RC)"
[ -e "$MB/build-session" ] && nope "records no build-session" || ok "records no build-session"

printf '\n== an older tandem without explain-reset still says something ==\n'
run STUB_RESET=sent STUB_NO_EXPLAIN=1
[ "$RC" -eq 0 ] && ok "still delivers" || nope "still delivers (rc $RC)"
has "says the raw token" "builder conversation: sent"

printf '\n== a builder that stays done never took the prompt (exit 7) ==\n'
# "done" is settled, not proof that a turn started.
run STUB_RESET=reset STUB_STATUS=done
[ "$RC" -eq 7 ] && ok "exits 7" || nope "exits 7 (rc $RC)"
hasno "does not claim delivery" "plan handed to"
[ -e "$MB/build-session" ] && nope "records no build-session" || ok "records no build-session"

printf '\n== an unconfirmed reset is never typed into (exit 7) ==\n'
# Unconfirmed means the pane may be at a dialog (Codex's /new picker), so
# nothing is sent.
run STUB_RESET=unconfirmed STUB_STATUS=working
[ "$RC" -eq 7 ] && ok "exits 7" || nope "exits 7 (rc $RC)"
grep -qx prompt "$CALLS" && nope "sends no prompt" || ok "sends no prompt"
has "names the pane to inspect" "Check pane"
hasno "does not claim delivery" "plan handed to"
[ -e "$MB/build-session" ] && nope "records no build-session" || ok "records no build-session"

printf '\n== tandem owns task reviews ==\n'
run HERDR_JUDGE=1 TYPESAFE_API_KEY=k
grep -q 'Tandem decides when a task gets a review' "$SENT" && ok "delivered prompt delegates reviews to tandem" || nope "delivered prompt delegates reviews to tandem"
run
grep -q 'Tandem decides' "$SENT" && nope "off prompt contains triage instructions" || ok "off prompt has no triage instructions"
grep -q 'The reviewer pane reviews each task' "$SENT" && ok "off prompt still sends reviews to the reviewer pane" || nope "off prompt still sends reviews to the reviewer pane"

printf '\n%s passed, %s failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

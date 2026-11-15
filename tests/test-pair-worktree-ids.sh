#!/usr/bin/env bash
# A worktree whose pair needs a checkout id, and a pair started before pair
# names came from the checkout, through the shipped scripts and a fake Herdr.
#
# Linked worktrees at a/proj and b/proj both slug to "proj", so their names use
# the checkout id that only tandem-pair writes to the state registry:
#   - a dry run shows a placeholder for the id and writes no registry;
#   - the real run binds the id and records the names in pair.json;
#   - a handback and a verdict sent from a sandbox, which reaches neither the
#     Herdr socket nor a writable state home, still reach that pair's planner.
# A linked worktree "up" paired under the plain names with no pair.json, as
# before this change:
#   - pairing again under those names records them and keeps the bridge;
#   - pairing under the derived names restarts the bridge for the new planner,
#     so a relayed handback reaches it.
set -uo pipefail
ROOT_DIR=$(cd "$(dirname "$0")/.." && pwd)

. "$ROOT_DIR/tests/lib/pair-worktrees.sh"

MAIN="$TMP/main"
mkdir -p "$MAIN" "$TMP/a" "$TMP/b"; git -C "$MAIN" init -q
git -C "$MAIN" config user.email t@t; git -C "$MAIN" config user.name t
printf '.tandem-relay/\n' > "$MAIN/.gitignore"
printf '{"builder_kind":"codex","reviewer":{"kind":"codex"}}\n' > "$MAIN/.tandem.json"
printf '# Plan\n\n**Goal:** g.\n\n### Task 1: First\n\nfirst thing\n' > "$MAIN/plan.md"
git -C "$MAIN" add . && git -C "$MAIN" commit -qm init
for w in a/proj b/proj up; do git -C "$MAIN" worktree add -q -b "br-${w%%/*}" "$TMP/$w" 2>/dev/null; done
WT_A=$(cd "$TMP/a/proj" && pwd -P); UP=$(cd "$TMP/up" && pwd -P)

printf 'w1:p1\t-\t%s\tclaude\tw1:t1\nw3:p1\t-\t%s\tclaude\tw3:t1\n' "$WT_A" "$UP" > "$FAKE/agents"
FAKE="$FAKE" PATH="$TMP/bin:$PATH" fake_socket_start "$TMP" || exit 1

# A Codex sandbox: herdr is on PATH but its socket is denied.
mkdir -p "$TMP/sbx"; ln -s "$TMP/bin/tandem" "$TMP/sbx/tandem"
printf '#!/bin/sh\necho %s >&2\nexit 1\n' "'Error: Os { code: 1, kind: PermissionDenied, message: \"Operation not permitted\" }'" > "$TMP/sbx/herdr"
chmod +x "$TMP/sbx/herdr"
# sandboxed DIR WHO CMD...: run CMD in DIR as the agent WHO from inside that
# sandbox, with a state home it can read but not write.
sandboxed() {
  local dir="$1" who="$2"; shift 2
  as_pane "$dir" "$(pane_of "$who")" env PATH="$TMP/sbx:$ROOT_DIR/bin:/usr/bin:/bin" \
    TANDEM_STATE_HOME="$TMP/state-ro" HERDR_SOCKET_PATH="$TMP/no.sock" HERDR_ROLE="$who" "$@"
}
# delivered_to NAME BEFORE: within 5 s the prompt count passes BEFORE and the
# latest prompt went to NAME.
delivered_to() {
  local _
  for _ in $(seq 1 50); do
    [ "$(prompts)" -gt "$2" ] && break; sleep 0.1
  done
  [ "$(prompts)" -eq $(($2 + 1)) ] && [ "$(last_to)" = "$1" ]
}
printf 'all done\n' > "$TMP/report.md"; printf 'looks clean\n' > "$TMP/review.md"

printf '\n== a dry run shows the id it does not have yet ==\n'
OUT=$(pair "$WT_A" w1:p1 --dry-run); rc=$?
[ "$rc" -eq 0 ] && printf '%s\n' "$OUT" | grep -qF 'planner-<id>' \
  && ok "the dry run names planner-<id>" || nope "dry run: rc $rc: $OUT"
[ ! -e "$TMP/state/registry.json" ] && ok "and writes no state registry" || nope "the dry run wrote $(ls -A "$TMP/state")"

printf '\n== the real run binds the id ==\n'
OUT=$(pair "$WT_A" w1:p1); rc=$?; fake_set_state idle
P=$(record_of "$WT_A" | sed -n 's/.*"planner":"\([^"]*\)".*/\1/p'); ID="${P#planner-}"
if [ "$rc" -eq 0 ] && [[ "$ID" =~ ^[0-9a-f]{6}$ ]] \
  && [ "$(record_of "$WT_A")" = "{\"planner\":\"planner-$ID\",\"builder\":\"builder-$ID\",\"reviewer\":\"reviewer-$ID\"}" ]; then
  ok "a/proj pairs as planner-$ID and records it"
else
  nope "a/proj pair: rc $rc, pair.json '$(record_of "$WT_A")': $OUT"
fi
[ "$(names_in w1:t1)" = "builder-$ID planner-$ID reviewer-$ID " ] && ok "its agents carry the id" || nope "agents: $(names_in w1:t1)"

printf '\n== a sandboxed handback and verdict reach the planner ==\n'
cp -R "$TMP/state" "$TMP/state-ro" && chmod -R a-w "$TMP/state-ro"
before=$(prompts)
OUT=$(sandboxed "$WT_A" "builder-$ID" "$ROOT_DIR/bin/tandem-handback" --report "$TMP/report.md" --plan "$WT_A/plan.md"); rc=$?
[ "$rc" -eq 0 ] && delivered_to "planner-$ID" "$before" \
  && ok "the builder's handback reaches planner-$ID" || nope "handback: rc $rc, last to '$(last_to)': $OUT"
fake_set_state idle; before=$(prompts)
OUT=$(sandboxed "$WT_A" "reviewer-$ID" "$ROOT_DIR/bin/tandem-verdict" --plan "$WT_A/plan.md" --review "$TMP/review.md" --verdict clean); rc=$?
[ "$rc" -eq 0 ] && delivered_to "planner-$ID" "$before" \
  && ok "the reviewer's verdict reaches planner-$ID" || nope "verdict: rc $rc, last to '$(last_to)': $OUT"
fake_set_state idle

printf '\n== a pair from before pair.json keeps working ==\n'
# plain_pair: pair up under the plain names.
plain_pair() { HERDR_PLANNER_NAME=planner HERDR_BUILDER_NAME=builder HERDR_REVIEWER_NAME=reviewer pair "$UP" w3:p1; }
OUT=$(plain_pair); rc=$?
fake_set_state idle
rm -f "$UP/.tandem-relay/pair.json"; OLD=$(bridge_of "$UP")
[ "$rc" -eq 0 ] && [ -n "$OLD" ] && ok "up runs a pair under the plain names, with no pair.json" || nope "old pair: rc $rc: $OUT"
# The guide's advice: pair once under the names the live pair runs under.
OUT=$(plain_pair); rc=$?
fake_set_state idle
[ "$rc" -eq 0 ] && [ "$(record_of "$UP")" = '{"planner":"planner","builder":"builder","reviewer":"reviewer"}' ] \
  && [ "$(bridge_of "$UP")" = "$OLD" ] && [ "$(names_in w3:t1)" = "builder planner reviewer " ] \
  && ok "pairing under those names records them and keeps the bridge" || nope "record: rc $rc, '$(record_of "$UP")': $OUT"
# Pairing under the derived names instead moves the bridge to the new planner.
rm -f "$UP/.tandem-relay/pair.json"
OUT=$(pair "$UP" w3:p1); rc=$?; fake_set_state idle
NEW=$(bridge_of "$UP")
case "$(ps -p "$NEW" -o command= 2>/dev/null)" in
  *"--planner planner-up"*) [ "$rc" -eq 0 ] && [ "$NEW" != "$OLD" ] && ok "pairing as planner-up restarts the bridge for it" \
    || nope "bridge: rc $rc, '$OLD' -> '$NEW': $OUT" ;;
  *) nope "the bridge still runs as: $(ps -p "$NEW" -o command= 2>/dev/null) (rc $rc): $OUT" ;;
esac
exits_within "$OLD" && ok "the old bridge stops" || nope "the old bridge '$OLD' still runs"
before=$(prompts)
OUT=$(sandboxed "$UP" builder-up "$ROOT_DIR/bin/tandem-handback" --report "$TMP/report.md" --plan "$UP/plan.md"); rc=$?
[ "$rc" -eq 0 ] && delivered_to planner-up "$before" \
  && ok "a relayed handback from builder-up reaches planner-up" || nope "handback: rc $rc, last to '$(last_to)': $OUT"

printf '\ntests/test-pair-worktree-ids: passed %d, failed %d\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

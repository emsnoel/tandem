#!/usr/bin/env bash
# A Codex reviewer's reply is read from its own session log (issue #86).
#
# Codex redraws its screen, so Herdr's read of the pane can start in the
# middle of a reply, past job_id and the hashes. The full reply is in the
# Codex session log, and tandem records it from there:
#   - a reply the pane shows only the end of is recorded from the log;
#   - a review_unknown pause names the ids and hashes a hand-written reply needs.
set -uo pipefail
. "$(cd "$(dirname "$0")" && pwd)/lib/records.sh"
ROOT_DIR=$(cd "$(dirname "$0")/.." && pwd)

PASS=0; FAIL=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }

command -v jq >/dev/null || { echo "need jq" >&2; exit 2; }
TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
(cd "$ROOT_DIR" && go build -o "$TMP/bin/tandem" ./cmd/tandem) || { echo "could not build tandem"; exit 1; }
ln -s "$(command -v jq)" "$TMP/bin/jq"

. "$ROOT_DIR/tests/lib/fake-herdr.sh"
. "$ROOT_DIR/tests/lib/fake-socket.sh"
FAKE="$TMP/fake"
fake_herdr_install "$TMP/bin" "$FAKE"
FAKE="$FAKE" PATH="$TMP/bin:$PATH" fake_socket_start "$TMP" || exit 1
# The reviewer keeps one conversation, so its prompts go to its own log.
printf 'rev-1\n' > "$FAKE/session.reviewer"

REPO="$TMP/repo"; mkdir -p "$REPO"; git -C "$REPO" init -q
git -C "$REPO" config user.email t@t; git -C "$REPO" config user.name t
printf '.tandem-relay/\n' > "$REPO/.gitignore"
mkdir -p "$REPO/.tandem-relay"; write_builder "$REPO/.tandem-relay"
printf '# Plan\n\n**Goal:** g.\n\n### Task 1: First\n\nfirst thing\n\n### Task 2: Second\n\nsecond thing\n' > "$REPO/plan.md"
git -C "$REPO" add . && git -C "$REPO" commit -qm plan
CODEX="$TMP/codex"
# ACK_MS, when set, shortens how long a delivered prompt waits for its reply.
td() { env PATH="$TMP/bin:/usr/bin:/bin" FAKE="$FAKE" REPO="$REPO" CODEX_HOME="$CODEX" TANDEM_BUILD_ENGINE=durable TANDEM_STATE_HOME="$TMP/state" \
  ${ACK_MS:+TANDEM_ACK_WINDOW_MS=$ACK_MS} tandem "$@"; }
tickn() { for _ in 1 2 3 4; do td build tick --root "$REPO" >/dev/null 2>"$TMP/err"; done; }
field() { printf '%s' "$1" | grep -o -- "--$2 [0-9a-f]*" | head -1 | cut -d' ' -f2; }
jfield() { printf '%s' "$1" | grep -o "\"$2\":\"[0-9a-f]*\"" | head -1 | cut -d'"' -f4; }
review_of() { td build status --root "$REPO" --json | jq -r ".tasks[$1].attempts[-1].review"; }

td build start --plan "$REPO/plan.md" --root "$REPO" --builder builder --planner planner --builder-kind codex \
  --reviewer reviewer --reviewer-kind codex --check true >/dev/null 2>"$TMP/err" || { nope "start: $(cat "$TMP/err")"; exit 1; }

# hand_back N [ACK_MS]: the builder takes task N and hands it back; the review
# prompt for it is typed into the reviewer pane, to wait ACK_MS for a reply.
# Prints the review prompt.
hand_back() {
  local n="$1" ack="${2:-}" pl att job eff nonce
  tickn
  pl=$(cut -f1,2- "$FAKE/prompts" | grep '^builder' | grep -v '/new' | sed -n "${n}p" | cut -f2-)
  att=$(field "$pl" attempt); job=$(field "$pl" job); eff=$(field "$pl" effect); nonce=$(field "$pl" nonce)
  td build event new --root "$REPO" --kind builder_started --task "$n" --attempt "$att" --job "$job" --effect "$eff" --nonce "$nonce" >/dev/null 2>&1
  printf 'work for task %s\n' "$n" > "$REPO/file$n.txt"
  printf 'done: task %s\n' "$n" > "$TMP/report$n.md"
  td build event new --root "$REPO" --kind builder_submitted --task "$n" --attempt "$att" --job "$job" --effect "$eff" --nonce "$nonce" --payload "$TMP/report$n.md" >/dev/null 2>&1
  fake_set_state idle
  ACK_MS="$ack" tickn
  grep '^reviewer' "$FAKE/prompts" | sed -n "${n}p" | cut -f2-
}

printf '\n== a reply the pane shows only the end of is recorded from the Codex log ==\n'
RV=$(hand_back 1)
RJ=$(jfield "$RV" job_id); RA=$(jfield "$RV" attempt_id); RP=$(jfield "$RV" patch_hash); RT=$(jfield "$RV" tree_hash)
[ -n "$RJ" ] && [ -n "$RT" ] && ok "the review prompt reached the reviewer pane" || { nope "no review prompt: $(cut -c1-60 "$FAKE/prompts" | tr '\n' '|')"; exit 1; }
LOG=""; for f in "$CODEX"/sessions/*/*/*/rollout-*-rev-1.jsonl; do [ -f "$f" ] && LOG=$f; done
[ -n "$LOG" ] && ok "the reviewer's prompt is in its Codex log" || { nope "no reviewer log under $CODEX"; exit 1; }
REPLY=$(jq -cn --arg j "$RJ" --arg a "$RA" --arg p "$RP" --arg t "$RT" \
  '{schema_version:1,job_id:$j,attempt_id:$a,patch_hash:$p,tree_hash:$t,status:"passed",findings:[]}')
jq -cn --arg r "$REPLY" '{type:"response_item",payload:{type:"message",role:"assistant",content:[{type:"output_text",text:$r}]}}' >> "$LOG"
# Herdr reads the pane from the middle of the reply on, as in the issue.
printf '%s\n\n  Worked for 30s\n' "${REPLY#*\"status\"}" > "$FAKE/pane"
fake_set_state idle
td build tick --root "$REPO" >/dev/null 2>"$TMP/err"
[ "$(review_of 0)" = passed ] && ok "the reply is recorded on the first tick after the reviewer goes idle" \
  || nope "review is $(review_of 0) after the tick: $(cat "$TMP/err")"

printf '\n== a review_unknown pause names what a hand-written reply needs ==\n'
rm -f "$FAKE/pane"
RV=$(hand_back 2 1)
RJ=$(jfield "$RV" job_id); RA=$(jfield "$RV" attempt_id); RP=$(jfield "$RV" patch_hash); RT=$(jfield "$RV" tree_hash)
[ -n "$RJ" ] || { nope "no second review prompt"; exit 1; }
fake_set_state idle
td build tick --root "$REPO" >/dev/null 2>&1
[ "$(review_of 1)" != passed ] && ok "no reply anywhere records nothing" || nope "a review was recorded with no reply"
ST=$(td build status --root "$REPO" --json | jq -r '.state + "|" + .pause_code')
[ "$ST" = "paused|review_unknown" ] && ok "the run pauses as review_unknown" || nope "state $ST"
OUT=$(env PATH="$TMP/bin:/usr/bin:/bin" FAKE="$FAKE" REPO="$REPO" CODEX_HOME="$CODEX" TANDEM_BUILD_ENGINE=durable TANDEM_STATE_HOME="$TMP/state" \
  "$ROOT_DIR/bin/tandem-pause-note" --root "$REPO" 2>&1)
for want in "job_id: $RJ" "attempt_id: $RA" "patch_hash: $RP" "tree_hash: $RT"; do
  case "$OUT" in *"$want"*) ok "the note gives $want" ;; *) nope "the note does not give $want: $OUT" ;; esac
done
case "$OUT" in *"$CODEX/sessions"*) ok "the note points at the Codex session log" ;; *) nope "the note does not name the Codex session log: $OUT" ;; esac

printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

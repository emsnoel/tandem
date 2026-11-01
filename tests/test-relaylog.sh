#!/usr/bin/env bash
# Every completed tandem-ask exchange leaves a .tandem-log/<id>.json record; a
# failed or timed-out one leaves none. tandem-relaylog reads them back, and
# tandem-init ignores the directory.
#
# Hermetic: tandem and herdr are stubs (SNAP_MODE picks the transport), jq is
# real, and a real temp git repo supplies the toplevel.
set -uo pipefail
ASK="${1:-$(cd "$(dirname "$0")/.." && pwd)/bin/tandem-ask}"
[ -x "$ASK" ] || { echo "not executable: $ASK" >&2; exit 2; }
BINDIR=$(cd "$(dirname "$ASK")" && pwd)
RELAYLOG="$BINDIR/tandem-relaylog"
INIT="$BINDIR/tandem-init"
PROJECT="$BINDIR/tandem-project"
[ -x "$RELAYLOG" ] || { echo "not executable: $RELAYLOG" >&2; exit 2; }
[ -x "$INIT" ]     || { echo "not executable: $INIT" >&2; exit 2; }

PASS=0; FAIL=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }
chk()  { case "$OUT" in *"$2"*) ok "$1" ;; *) nope "$1 (missing: $2)" ;; esac; }

TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
mkdir -p "$TMP/bin"
REPO="$TMP/repo"

# SNAP_MODE=direct: snapshot exits 0. mailbox: exits 3. timeout: exits 3 and
# nothing answers. PROMPT_FAIL=1 makes the direct `agent prompt` fail.
# RELAY_LOG_FAIL=1 makes `relay log` fail; otherwise the real binary writes.
export REAL_TANDEM; REAL_TANDEM=$(command -v tandem) || { echo "need an installed tandem" >&2; exit 2; }
cat > "$TMP/bin/tandem" <<'STUB'
#!/usr/bin/env bash
case "$1 $2" in
  "relay log")   [ "${RELAY_LOG_FAIL:-0}" = 1 ] && exit 1; exec "$REAL_TANDEM" "$@" ;;
  "relay write") exec "$REAL_TANDEM" "$@" ;;
  "agent snapshot")
    case "${SNAP_MODE:-direct}" in
      direct) printf 'p9\t/repo\tidle\n'; exit 0 ;;
      *)      exit 3 ;;
    esac
    ;;
  "agent prompt")
    if [ "${PROMPT_FAIL:-0}" = 1 ]; then
      printf 'agent_prompt_stalled\tno lifecycle change\n' >&2
      exit 1
    fi
    exit 0 ;;
  "agent status") printf 'idle\n'; exit 0 ;;
  "agent read")   printf '%s\n' "${DIRECT_ANSWER:-the answer is 42}"; exit 0 ;;
  "config check-command") printf '{"check_command":"","check_paths":[]}\n'; exit 0 ;;
esac
if [ "$1" = "paths" ] && { [ "$2" = "relay" ] || [ "$2" = "manifest" ]; }; then
  which="$2"; shift 2
  root=""
  while [ $# -gt 0 ]; do case "$1" in --root) root="$2"; shift 2 ;; *) shift ;; esac; done
  case "$which" in
    relay)    printf '%s/.tandem-relay\n' "$root" ;;
    manifest) printf '%s/.tandem.json\n' "$root" ;;
  esac
  exit 0
fi
exit 0
STUB
chmod +x "$TMP/bin/tandem"

cat > "$TMP/bin/herdr" <<'STUB'
#!/usr/bin/env bash
case "$1 $2" in
  "agent list") printf '{"result":{"agents":[{"pane_id":"pX","name":"peer","agent":"codex","agent_status":"idle","cwd":"/repo"}]}}' ;;
  *) printf '{}' ;;
esac
STUB
chmod +x "$TMP/bin/herdr"

newrepo() { rm -rf "$REPO"; mkdir -p "$REPO"; git -C "$REPO" init -q; }
run_ask() {
  OUT=$(cd "$REPO" && PATH="$TMP/bin:$PATH" HERDR_ENV=1 HERDR_PANE_ID=pX HERDR_ROLE=builder \
    SNAP_MODE="${SNAP_MODE:-direct}" PROMPT_FAIL="${PROMPT_FAIL:-0}" DIRECT_ANSWER="${DIRECT_ANSWER:-}" \
    RELAY_LOG_FAIL="${RELAY_LOG_FAIL:-0}" \
    "$ASK" "$@" 2>&1); RC=$?
}
log_count() { find "$REPO/.tandem-log" -maxdepth 1 -name '*.json' 2>/dev/null | wc -l | tr -d ' '; }

echo "--- direct mode: a completed exchange is logged ---"
newrepo
SNAP_MODE=direct run_ask planner "what is the answer?"
[ "$(log_count)" = 1 ] && ok "exactly one log record was written" || nope "expected 1 record, got $(log_count)"
REC=$(find "$REPO/.tandem-log" -name '*.json' | head -1)
[ -n "$REC" ] && OUT=$(cat "$REC") || OUT=""
chk "record names the question"    "what is the answer?"
chk "record names the answer"      "the answer is 42"
chk "record names the sender"      '"from": "peer"'
chk "record names the target"      '"to": "planner"'
grep -q '"ts":' "$REC" 2>/dev/null && ok "record has a timestamp" || nope "record missing a timestamp"
grep -q '"id":' "$REC" 2>/dev/null && ok "record has an id" || nope "record missing an id"

echo "--- the log is best effort: a failed write changes nothing ---"
newrepo
RELAY_LOG_FAIL=1 SNAP_MODE=direct run_ask planner "is the log optional?"
[ "$RC" -eq 0 ] && ok "the question still exits 0" || nope "a failed log write failed the question (rc $RC)"
chk "and the answer still prints" "the answer is 42"
[ "$(log_count)" = 0 ] && ok "and no record was written" || nope "found $(log_count) records"

echo "--- direct mode: a failed exchange logs nothing ---"
newrepo
SNAP_MODE=direct PROMPT_FAIL=1 run_ask planner "will this fail?"
[ "$RC" -ne 0 ] && ok "the failed call itself reports failure" || nope "expected a nonzero exit"
[ "$(log_count)" = 0 ] && ok "no log record was written for a failed exchange" \
                        || nope "a failed exchange should not be logged (found $(log_count))"

echo "--- mailbox mode: a completed exchange is logged, sharing the q/a id ---"
newrepo
(
  sleep 1
  QFILE=$(find "$REPO/.tandem-relay/q" -name '*.json' 2>/dev/null | head -1)
  if [ -n "$QFILE" ]; then
    id=$(basename "$QFILE" .json)
    printf 'the mailbox answer' > "$REPO/.tandem-relay/a/$id.md"
  fi
) &
SNAP_MODE=mailbox run_ask --timeout 5 planner "mailbox question"
wait
[ "$(log_count)" = 1 ] && ok "exactly one log record was written" || nope "expected 1 record, got $(log_count)"
QID=$(find "$REPO/.tandem-relay/q" -name '*.json' | head -1 | xargs -I{} basename {} .json)
LOGID=$(find "$REPO/.tandem-log" -name '*.json' | head -1 | xargs -I{} basename {} .json)
[ -n "$QID" ] && [ "$QID" = "$LOGID" ] && ok "the log record shares the mailbox exchange's own id" \
                                        || nope "id mismatch: mailbox=$QID log=$LOGID"
REC=$(find "$REPO/.tandem-log" -name '*.json' | head -1)
OUT=$(cat "$REC" 2>/dev/null || true)
chk "record names the mailbox question" "mailbox question"
chk "record names the mailbox answer"   "the mailbox answer"

echo "--- mailbox mode: a timeout logs nothing ---"
newrepo
SNAP_MODE=mailbox run_ask --timeout 1 planner "nobody will answer this"
[ "$RC" -ne 0 ] && ok "the timed-out call itself reports failure" || nope "expected a nonzero exit"
[ "$(log_count)" = 0 ] && ok "no log record was written for a timeout" \
                        || nope "a timed-out exchange should not be logged (found $(log_count))"

echo "--- direct mode also ignores .tandem-log in .gitignore ---"
newrepo
SNAP_MODE=direct run_ask planner "does this get ignored?"
grep -qE '^/?\.tandem-log/?$' "$REPO/.gitignore" 2>/dev/null && ok "tandem-ask adds .tandem-log/ to .gitignore" \
                                                               || nope ".gitignore missing .tandem-log/ entry"
git -C "$REPO" status --porcelain | grep -q '\.tandem-log' \
  && nope "git genuinely ignores the log directory" || ok "git genuinely ignores the log directory"

echo "--- a write failure while logging must not fail the exchange itself ---"
# A .gitignore that is a directory makes the append fail. Under set -e that
# would abort tandem-ask unless the call is guarded.
newrepo
mkdir -p "$REPO/.gitignore"
SNAP_MODE=direct run_ask planner "does a bad .gitignore still let this succeed?"
[ "$RC" -eq 0 ] && ok "the exchange still succeeds despite an unwritable .gitignore" \
                 || nope "a logging failure must never fail the exchange (got exit $RC)"
chk "the reply still printed" "the answer is 42"

echo "--- tandem-relaylog: no history yet ---"
newrepo
OUT=$(cd "$REPO" && PATH="$TMP/bin:$PATH" "$RELAYLOG" 2>&1); RC=$?
[ "$RC" -eq 0 ] && ok "exits 0 with no history" || nope "should exit 0 with no history (got $RC)"
chk "names where it looked" "no history yet"

echo "--- tandem-relaylog: formatting, --limit and --json ---"
newrepo
mkdir -p "$REPO/.tandem-log"
for i in 1 2 3; do
  jq -n --arg id "2026091912000$i-1-1" --arg ts "2026-09-19T12:00:0${i}Z" \
     --arg from builder --arg to planner --arg q "question $i" --arg a "answer $i" \
     '{id:$id, ts:$ts, from:$from, to:$to, question:$q, answer:$a}' \
     > "$REPO/.tandem-log/2026091912000$i-1-1.json"
done
OUT=$(cd "$REPO" && "$RELAYLOG" 2>&1)
chk "prints the first record"  "question 1"
chk "prints the last record"   "question 3"
FIRSTLINE=$(printf '%s\n' "$OUT" | head -1)
case "$FIRSTLINE" in
  *20260919120001*) ok "prints oldest first" ;;
  *) nope "expected the oldest record first, got: $FIRSTLINE" ;;
esac

OUT=$(cd "$REPO" && "$RELAYLOG" --limit 2 2>&1)
case "$OUT" in
  *"question 1"*) nope "--limit 2 should drop the oldest record" ;;
  *) ok "--limit 2 drops the oldest record" ;;
esac
chk "--limit 2 keeps the most recent" "question 3"

OUT=$(cd "$REPO" && "$RELAYLOG" --json 2>&1)
chk "--json prints raw records" '"question":"question 2"'
JSONLINES=$(printf '%s\n' "$OUT" | grep -c .)
[ "$JSONLINES" = 3 ] && ok "--json prints exactly one line per record" \
                      || nope "--json should print 3 lines for 3 records, got $JSONLINES"
# A herestring keeps ALL_VALID visible after the loop; a pipe runs it in a
# subshell.
ALL_VALID=1
while IFS= read -r line; do
  [ -z "$line" ] && continue
  jq -e . >/dev/null 2>&1 <<<"$line" || ALL_VALID=0
done <<<"$OUT"
[ "$ALL_VALID" = 1 ] && ok "every --json line parses as one JSON object" \
                      || nope "every --json line must parse as one JSON object"

echo "--- judge records are not Q&A ---"
JREPO="$TMP/judge-repo"
mkdir -p "$JREPO/.tandem-log"
git -C "$JREPO" init -q
printf '{"id":"20260929-002126-1-1","task_id":"1","answers":{}}\n' > "$JREPO/.tandem-log/judge-20260929-002126-1-1.json"
OUT=$(cd "$JREPO" && "$RELAYLOG" 2>&1)
chk "only judge records: says there is no history" "no history yet"
case "$OUT" in *"Q: null"*) nope "a judge record printed as Q: null" ;; *) ok "no judge record printed as Q&A" ;; esac
printf '{"id":"20260929-002222-2-2","ts":"t","from":"planner","to":"builder","question":"q1","answer":"a1"}\n' \
  > "$JREPO/.tandem-log/20260929-002222-2-2.json"
OUT=$(cd "$JREPO" && "$RELAYLOG" 2>&1)
chk "the ask record is printed" "Q: q1"
case "$OUT" in *"Q: null"*) nope "a judge record printed beside the ask" ;; *) ok "the judge record stays out" ;; esac

echo "--- tandem-init also ignores .tandem-log ---"
[ -x "$PROJECT" ] || { echo "not executable: $PROJECT" >&2; exit 2; }
newrepo
OUT=$(cd "$REPO" && PATH="$BINDIR:$TMP/bin:$PATH" "$INIT" 2>&1); RC=$?
[ "$RC" -eq 0 ] && ok "tandem-init exits 0" || nope "tandem-init exits 0 (got $RC): $OUT"
grep -qE '^/?\.tandem-log/?$' "$REPO/.gitignore" 2>/dev/null && ok "tandem-init adds .tandem-log/ to .gitignore" \
                                                               || nope "tandem-init did not add .tandem-log/"
OUT=$(cd "$REPO" && PATH="$BINDIR:$TMP/bin:$PATH" "$INIT" --force 2>&1)
chk "re-running tandem-init is idempotent" "already ignores .tandem-log/"

printf '\nrelaylog: passed %d, failed %d\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

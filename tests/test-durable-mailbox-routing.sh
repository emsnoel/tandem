#!/usr/bin/env bash
# Protocol 2 routing in tandem-handback and tandem-bridge.
#
# A durable run is marked by <relay>/active-run.json, which both look for before
# any legacy build.json. `tandem build event new` is the real binary; Herdr and
# `build event ingest` are stubbed, and only ingest's arguments are checked.
set -uo pipefail
ROOT_DIR=$(cd "$(dirname "$0")/.." && pwd)
HANDBACK="$ROOT_DIR/bin/tandem-handback"
BRIDGE="$ROOT_DIR/bin/tandem-bridge"

PASS=0; FAIL=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }
has()  { case "$2" in *"$3"*) ok "$1" ;; *) nope "$1 (missing: $3)" ;; esac; }
hasnt(){ case "$2" in *"$3"*) nope "$1 (present: $3)" ;; *) ok "$1" ;; esac; }

command -v jq >/dev/null || { echo "need jq" >&2; exit 2; }
TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
(cd "$ROOT_DIR" && go build -o "$TMP/tandem-real" ./cmd/tandem) || { echo "could not build tandem"; exit 1; }
mkdir -p "$TMP/bin"
# A jq that fails: tandem-handback and tandem-bridge ask tandem, so any jq
# call is a bug. This file's own checks use the real jq.
printf '#!/bin/sh\necho "jq was called: $*" >&2\nexit 99\n' > "$TMP/bin/jq"
chmod +x "$TMP/bin/jq"

RUN=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa; ATT=cccccccccccccccccccccccccccccccc; JOB=dddddddddddddddddddddddddddddddd
EFF=eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee; NONCE=ffffffffffffffffffffffffffffffff
D=(--run "$RUN" --attempt "$ATT" --job "$JOB" --effect "$EFF" --nonce "$NONCE")

# --- stubs --------------------------------------------------------------------
cat > "$TMP/bin/herdr" <<'STUB'
#!/usr/bin/env bash
[[ "$1 $2" == "agent list" ]] && { printf '{"result":{"agents":[]}}\n'; exit 0; }
exit 0
STUB
# tandem: relay paths from --root; `build event new` is the real binary;
# `build event ingest` and `agent status` are scripted and logged.
cat > "$TMP/bin/tandem" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$CALLS"
case "$1" in
  paths) shift; while (( $# )); do [[ "$1" == --root ]] && { printf '%s/.tandem-relay\n' "$2"; exit 0; }; shift; done; exit 1 ;;
  build)
    case "$2" in
      # TICK_OUT and STATUS_OUT are the --fields answers, separated by "|".
      tick|recover) printf '%s|' "${TICK_OUT:-x}" | tr '|' '\000'; exit "${TICK_RC:-0}" ;;
      status) [ -n "${STATUS_OUT:-}" ] && printf '%s|' "$STATUS_OUT" | tr '|' '\000'; exit 0 ;;
    esac
    [[ "$2" == roles ]] && exec "$TANDEM_REAL" "$@"
    case "$2 $3" in
      "event new") exec "$TANDEM_REAL" "$@" ;;
      "event ingest") printf 'committed\0\0'; exit "${INGEST_RC:-0}" ;;
    esac ;;
  relay|state) exec "$TANDEM_REAL" "$@" ;;
  agent)
    [[ "$2" == status ]] && { printf '%s\n' "${BUILDER_STATUS:-idle}"; exit 0; }
    # What reaches the planner, one message per file.
    [[ "$2" == prompt && -n "${PROMPTS:-}" ]] && { mkdir -p "$PROMPTS"; printf '%s' "$4" > "$PROMPTS/$(ls "$PROMPTS" | wc -l | tr -d ' ')"; } ;;
esac
exit 0
STUB
chmod +x "$TMP/bin"/*

# --- fixtures -----------------------------------------------------------------
fixture() { # fixture <dir>: a git repo whose relay carries a durable run
  local r="$1"; rm -rf "$r"; mkdir -p "$r/sub"; git -C "$r" init -q .
  mark "$r/.tandem-relay" "$RUN"
}
mark() { # mark <relay> <run>
  local relay="$1" run="$2"
  mkdir -p "$relay/durable/$run"/{q,payloads,receipts,quarantine}
  printf '{"schema_version":2,"run_id":"%s","tasks":[{"ordinal":1,"id":"receipt-store"},{"ordinal":2,"id":"second-task"}]}\n' "$run" > "$relay/durable/$run/manifest.json"
  printf '{"engine_version":2,"protocol_version":2,"run_id":"%s","project_id":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}\n' "$run" > "$relay/active-run.json"
}
CALLS="$TMP/calls"
HB_ENV=()
hb() { # hb <cwd> <args...>
  local cwd="$1"; shift; : > "$CALLS"
  OUT=$(cd "$cwd" && env -i PATH="$TMP/bin:/usr/bin:/bin" HOME="$HOME" CALLS="$CALLS" \
          TANDEM_REAL="$TMP/tandem-real" ${HB_ENV[@]+"${HB_ENV[@]}"} "$HANDBACK" "$@" 2>&1); RC=$?
}
REPORT="$TMP/report.md"; printf 'implemented, check green\n' > "$REPORT"
queued() { ls "$1"/q/*.json 2>/dev/null | head -1; }

printf '== a durable run takes protocol 2 ==\n'
R="$TMP/repo"; fixture "$R"
hb "$R" --task 2 "${D[@]}" --report "$REPORT"
[ "$RC" -eq 0 ] && ok "a durable handback exits 0" || nope "a durable handback exits 0 (rc $RC): $OUT"
Q=$(queued "$R/.tandem-relay/durable/$RUN")
if [ -n "$Q" ] && jq -e --arg a "$ATT" --arg n "$NONCE" '.schema_version==2 and .kind=="builder_submitted" and .task_id=="second-task" and .attempt_id==$a and .nonce==$n and (.event_id|test("^[0-9a-f]{32}$"))' "$Q" >/dev/null; then
  ok "it queues a versioned envelope with a random event id, the task slug and the nonce"
else nope "it queues a versioned envelope ($(cat "$Q" 2>/dev/null))"; fi
PP=$(jq -r '.payload_path' "$Q" 2>/dev/null)
[ "$(cat "$R/.tandem-relay/durable/$RUN/$PP" 2>/dev/null)" = "implemented, check green" ] && ok "the report travels as the payload" || nope "the report travels as the payload"
[ -e "$R/.tandem-relay/q" ] && [ -n "$(ls "$R/.tandem-relay/q" 2>/dev/null)" ] && nope "a legacy queue file was also written" || ok "no legacy queue file is written"
[ -e "$R/.tandem-relay/build/build.json" ] && nope "a legacy build.json appeared" || ok "no legacy build.json appears"
has "it tells the builder to stop" "$OUT" "Stop here"

rm -f "$R/.tandem-relay/durable/$RUN"/q/*; rm -f "$R/.tandem-relay/durable/$RUN"/payloads/*
hb "$R" --task 2 "${D[@]}" --started
[ "$RC" -eq 0 ] && ok "--started needs no report" || nope "--started needs no report (rc $RC): $OUT"
Q=$(queued "$R/.tandem-relay/durable/$RUN")
jq -e '.kind=="builder_started" and .task_id=="second-task"' "$Q" >/dev/null 2>&1 && ok "--started queues builder_started for the resolved task" || nope "--started envelope ($(cat "$Q" 2>/dev/null))"

rm -f "$R/.tandem-relay/durable/$RUN"/q/*
hb "$R" --task 1 "${D[@]}" --stuck --report "$REPORT"
Q=$(queued "$R/.tandem-relay/durable/$RUN")
jq -e '.kind=="builder_stuck"' "$Q" >/dev/null 2>&1 && ok "--stuck queues builder_stuck" || nope "--stuck envelope"

printf '\n== durable acknowledgements ignore pane and engine variables ==\n'
for context in absent empty zero legacy-engine; do
  fixture "$R"
  case "$context" in
    absent) HB_ENV=() ;;
    empty) HB_ENV=(HERDR_ENV=) ;;
    zero) HB_ENV=(HERDR_ENV=0) ;;
    legacy-engine) HB_ENV=(TANDEM_BUILD_ENGINE=legacy) ;;
  esac
  hb "$R" --task 2 "${D[@]}" --started
  Q=$(queued "$R/.tandem-relay/durable/$RUN")
  if [ "$RC" -eq 0 ] && [ -n "$Q" ] && jq -e '.kind=="builder_started" and .task_id=="second-task"' "$Q" >/dev/null; then
    ok "task 2 acknowledges with $context environment"
  else nope "task 2 acknowledgement with $context environment (rc $RC): $OUT"; fi
done
HB_ENV=()

# Check all anchors, including legacy report locations and durable payloads.
no_writes() {
  local label="$1" root="$2" files
  files=$(find "$root" -type f \( -path '*/q/*' -o -path '*/payloads/*' -o -path '*/reports/*' -o -name '*.report.md' -o -name '*.review.md' -o -name '.payload.*' \))
  [ -z "$files" ] && ok "$label writes no events, reports or payloads" || nope "$label wrote: $files"
}
refused() {
  local label="$1" reason="$2" root="$3"
  [ "$RC" -ne 0 ] && ok "$label is refused" || nope "$label was accepted"
  has "$label names the reason" "$OUT" "$reason"
  no_writes "$label" "$root"
}

printf '\n== the old form and wrong identities are refused ==\n'
fixture "$R"
hb "$R" --task 1 --report "$REPORT"
refused "legacy-style durable handback" "durable run" "$R"
fixture "$R"
hb "$R" --task 1 --run bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb --attempt "$ATT" --job "$JOB" --effect "$EFF" --nonce "$NONCE" --report "$REPORT"
refused "different run id" "not the active run" "$R"
fixture "$R"
hb "$R" --task 1 --run "$RUN" --report "$REPORT"
refused "incomplete identity" "needs --attempt, --job, --effect and --nonce" "$R"
fixture "$R"
hb "$R" --task 1 "${D[@]}" --nonce malformed --started
refused "malformed nonce" "nonce must be 32 lowercase hex characters" "$R"
fixture "$R"
hb "$R" --task 1 "${D[@]}" --review --report "$REPORT"
refused "durable --review" "review hand backs are not supported" "$R"
fixture "$R"
hb "$R" --task 9 "${D[@]}" --report "$REPORT"
refused "unknown task" "no task 9" "$R"
fixture "$R"
EMPTY="$TMP/empty.md"; printf '  \n' > "$EMPTY"
hb "$R" --task 1 "${D[@]}" --report "$EMPTY"
refused "empty report" "the report is empty" "$R"
fixture "$R"
hb "$R" --task 1 "${D[@]}" --report "$REPORT" --dry-run
[ "$RC" -eq 0 ] && ok "--dry-run succeeds" || nope "--dry-run failed: $OUT"
no_writes "--dry-run" "$R"

printf '\n== the marker is never ignored ==\n'
fixture "$R"
echo '{broken' > "$R/.tandem-relay/active-run.json"
hb "$R" --task 1 "${D[@]}" --report "$REPORT"
refused "corrupt marker" "not a readable durable-run marker" "$R"
fixture "$R"
echo '{broken' > "$R/.tandem-relay/active-run.json"
hb "$R" --task 1 --report "$REPORT"
refused "corrupt marker with legacy handback" "not a readable durable-run marker" "$R"

printf '\n== both anchors in a monorepo layout ==\n'
fixture "$R"; rm -f "$R/.tandem-relay/active-run.json"
mark "$R/sub/.tandem-relay" "$RUN"
hb "$R/sub" --task 1 "${D[@]}" --report "$REPORT"
[ "$RC" -eq 0 ] && [ -n "$(queued "$R/sub/.tandem-relay/durable/$RUN")" ] && ok "a marker at the builder's own directory is found" || nope "the subdirectory anchor was missed (rc $RC): $OUT"
fixture "$R"
mark "$R/sub/.tandem-relay" bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
hb "$R/sub" --task 1 "${D[@]}" --report "$REPORT"
refused "conflicting markers" "two durable runs" "$R"

printf '\n== durable flags need a durable run; legacy builds are unchanged ==\n'
L="$TMP/legacy"; rm -rf "$L"; mkdir -p "$L/.tandem-relay/build"; git -C "$L" init -q .
hb "$L" --task 1 "${D[@]}" --report "$REPORT"
refused "v2 flags without a durable run" "none is active" "$L"
printf '{"state":"running"}\n' > "$L/.tandem-relay/build/build.json"
HB_ENV=(TANDEM_BUILD_ENGINE=durable)
hb "$L" --task 2 --report "$REPORT"
refused "engine variable alone" "HERDR_ENV != 1" "$L"
HB_ENV=(HERDR_ENV=1 HERDR_ROLE=builder)
hb "$L" --task 2 --report "$REPORT"
HB_ENV=()
Q=$(ls "$L/.tandem-relay/q"/*.json 2>/dev/null | head -1)
if [ "$RC" -eq 0 ] && [ -n "$Q" ] && jq -e '.kind=="task" and .task==2 and .stuck==false' "$Q" >/dev/null 2>&1; then
  ok "a legacy build still takes the v1 task message"
else nope "legacy handback changed (rc $RC): $OUT"; fi
[ -z "$(ls "$L/.tandem-relay/durable" 2>/dev/null)" ] && ok "and writes nothing durable" || nope "legacy handback wrote durable files"

# --- the bridge ---------------------------------------------------------------
br() { # br <relay>
  : > "$CALLS"
  OUT=$(env -i PATH="$TMP/bin:/usr/bin:/bin" HOME="$HOME" HERDR_ENV=1 CALLS="$CALLS" TANDEM_REAL="$TMP/tandem-real" \
          BUILDER_STATUS="${BUILDER_STATUS:-idle}" INGEST_RC="${INGEST_RC:-0}" TICK_RC="${TICK_RC:-0}" \
          TICK_OUT="${TICK_OUT:-}" STATUS_OUT="${STATUS_OUT:-}" PROMPTS="${PROMPTS:-}" \
          "$BRIDGE" --mailbox "$1" --planner planner --once 2>&1); RC=$?
}
printf '\n== the bridge ingests protocol 2 and refuses protocol 1 under a durable run ==\n'
B="$TMP/brepo"; fixture "$B"; REL="$B/.tandem-relay"; RD="$REL/durable/$RUN"
printf '{"schema_version":2,"kind":"builder_started"}\n' > "$RD/q/e1.json"
BUILDER_STATUS=idle br "$REL"
grep -q -- "build event ingest --root $B --claimed $RD/q/e1.json.taken --session-settled" "$CALLS" \
  && ok "a queued v2 message is claimed and ingested, with the settled flag when the builder is idle" || nope "ingest call ($(cat "$CALLS"))"
[ -f "$RD/q/e1.json.taken" ] && [ ! -f "$RD/q/e1.json" ] && ok "it stays claimed until ingestion removes it" || nope "claim state"
has "the log names what ingest did" "$OUT" "e1.json.taken: committed"

printf '{"schema_version":2,"kind":"builder_submitted"}\n' > "$RD/q/e2.json"
BUILDER_STATUS=working br "$REL"
grep -q -- "claimed $RD/q/e2.json.taken" "$CALLS" && ! grep -- "claimed $RD/q/e2.json.taken" "$CALLS" | grep -q -- "--session-settled" \
  && ok "a working builder is not reported as settled" || nope "the settled flag was passed for a working builder"

printf '{"schema_version":2,"kind":"builder_submitted"}\n' > "$RD/q/e3.json"
INGEST_RC=1 br "$REL"
[ -f "$RD/q/e3.json.taken" ] && [ ! -f "$RD/q/e3.json" ] \
  && ok "a message that could not be decided yet stays claimed, where the queue watch does not see it" || nope "claim state after a failed ingest"
INGEST_RC=0 br "$REL"
grep -q -- "claimed $RD/q/e3.json.taken" "$CALLS" && ok "and the next pass ingests it again" || nope "a parked claim was not retried ($(cat "$CALLS"))"

rm -f "$RD"/q/*
printf '{"schema_version":2,"kind":"builder_started"}\n' > "$RD/q/e4.json.taken"
br "$REL"
[ "$(grep -c 'build event ingest' "$CALLS")" -eq 1 ] && grep -q -- "claimed $RD/q/e4.json.taken" "$CALLS" \
  && ok "a message claimed before a crash is replayed once at startup" || nope "startup replay ($(cat "$CALLS"))"

mkdir -p "$REL/q"; printf '{"id":"t1","kind":"task","from":"builder","question":"task","task":1,"stuck":false,"report_path":"/x","created":"2026-09-29T12:00:00Z"}\n' > "$REL/q/t1.json"
br "$REL"
grep -q 'build advance' "$CALLS" && nope "a v1 task message drove a durable run" || ok "a v1 task message does not drive a durable run"
[ -f "$REL/quarantine-v1/t1.json.taken" ] && grep -q "durable run $RUN" "$REL/quarantine-v1/t1.json.taken.reason" 2>/dev/null \
  && ok "it is set aside with a reason, not deleted" || nope "the v1 message was not quarantined with a reason"

printf '\n== the bridge drives a durable run through tick and recover, and a legacy relay through neither ==\n'
rm -rf "$REL/q" "$REL/quarantine-v1"; rm -f "$RD"/q/*
br "$REL"
[ "$(grep -c "^build recover --root $B" "$CALLS")" -eq 1 ] && ok "startup runs recover once for the durable run" || nope "recover calls ($(cat "$CALLS"))"
[ "$(grep -c "^build tick --root $B" "$CALLS")" -eq 1 ] && ok "each poll runs one tick" || nope "tick calls ($(cat "$CALLS"))"
first=$(grep -n '^build recover' "$CALLS" | head -1 | cut -d: -f1); second=$(grep -n '^build tick' "$CALLS" | head -1 | cut -d: -f1)
[ -n "$first" ] && [ -n "$second" ] && [ "$first" -lt "$second" ] && ok "recover comes before the first tick" || nope "order"
printf '{"schema_version":2,"kind":"builder_started"}\n' > "$RD/q/e5.json"
br "$REL"
ing=$(grep -n 'build event ingest' "$CALLS" | head -1 | cut -d: -f1); tk=$(grep -n '^build tick' "$CALLS" | head -1 | cut -d: -f1)
[ -n "$ing" ] && [ -n "$tk" ] && [ "$ing" -lt "$tk" ] && ok "a poll ingests queued messages before it ticks" || nope "ingest/tick order ($(cat "$CALLS"))"
TICK_RC=3 br "$REL"
case "$OUT" in *"build tick: exit 3"*) [ "$RC" -eq 0 ] && ok "a tick that fails is logged and the bridge keeps running" || nope "a failed tick stopped the bridge (rc $RC)" ;; *) nope "the failed tick was not logged ($OUT)" ;; esac
printf '\n== the bridge tells the planner once when a run pauses ==\n'
PROMPTS="$TMP/prompts"; rm -rf "$PROMPTS" "$RD/pause-reported"; rm -f "$RD"/q/*
sent() { ls "$PROMPTS" 2>/dev/null | wc -l | tr -d ' '; }
last() { cat "$PROMPTS/$(( $(sent) - 1 ))" 2>/dev/null; }
PAUSED="$RUN|paused|dispatch_failed"
RUNNING="$RUN|running"
STATUS_OUT=$'paused|dispatch_failed||builder\'s turn ended as soon as it started. Its pane shows:\nThere\'s an issue with the selected model (sonnet-5.5).'
export PROMPTS STATUS_OUT
TICK_OUT="$PAUSED" br "$REL"
[ "$(sent)" = 1 ] && ok "a run that paused is reported to the planner once, though recover and tick both saw it" || nope "messages sent: $(sent) ($OUT)"
has "the tick is logged with its run, state and code" "$OUT" "build tick: run $RUN paused (dispatch_failed)"
has "the message names the pause" "$(last)" "paused (dispatch_failed)"
has "it carries the reason" "$(last)" "There's an issue with the selected model (sonnet-5.5)."
has "and the way on" "$(last)" "tandem build stop --root $B"
has "then ends the stopped run's open jobs" "$(last)" "tandem state recover --run $RUN --root $B"
has "then archives it" "$(last)" "tandem state archive --run $RUN --root $B"
has "and re-pairs with a reviewer before handing off" "$(last)" "tandem-pair --reviewer KIND"
TICK_OUT="$PAUSED" br "$REL"
[ "$(sent)" = 1 ] && ok "the same pause is not reported again, across a restart" || nope "messages sent: $(sent)"
TICK_OUT="$RUNNING" br "$REL"
TICK_OUT="$PAUSED" br "$REL"
[ "$(sent)" = 2 ] && ok "a run that resumed and paused again is reported again" || nope "messages sent: $(sent)"
STATUS_OUT='paused|review_unknown'
TICK_OUT="$RUN|paused|review_unknown" br "$REL"
[ "$(sent)" = 3 ] && has "any pause is reported, not only a failed dispatch" "$(last)" "paused (review_unknown)" || nope "messages sent: $(sent)"
hasnt "a pause with no stop advice is not told to stop" "$(last)" "tandem build stop"
unset PROMPTS STATUS_OUT

LG="$TMP/legacyrelay"; rm -rf "$LG"; mkdir -p "$LG/.tandem-relay/q"; git -C "$LG" init -q .
br "$LG/.tandem-relay"
grep -qE '^build (tick|recover)' "$CALLS" && nope "a legacy relay was ticked" || ok "a legacy relay is never ticked or recovered"

printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

#!/usr/bin/env bash
# Review handback through tandem-handback.
#
# A reviewer hands back with --review-started and --review-result FILE. The
# message version comes from the protocol the run started with, never the
# environment: version 2 takes no review_started, version 3 does. Herdr and
# the controller are stubbed; `build event new` is the real binary.
set -uo pipefail
ROOT_DIR=$(cd "$(dirname "$0")/.." && pwd)
HANDBACK="$ROOT_DIR/bin/tandem-handback"

PASS=0; FAIL=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }
has()  { case "$2" in *"$3"*) ok "$1" ;; *) nope "$1 (missing: $3)" ;; esac; }

command -v jq >/dev/null || { echo "need jq" >&2; exit 2; }
TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
(cd "$ROOT_DIR" && go build -o "$TMP/tandem-real" ./cmd/tandem) || { echo "could not build tandem"; exit 1; }
mkdir -p "$TMP/bin"
# A jq that fails: tandem-handback asks tandem, so any jq call is a bug.
printf '#!/bin/sh\necho "jq was called: $*" >&2\nexit 99\n' > "$TMP/bin/jq"

RUN=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa; ATT=cccccccccccccccccccccccccccccccc; JOB=dddddddddddddddddddddddddddddddd
EFF=eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee; NONCE=ffffffffffffffffffffffffffffffff
D=(--run "$RUN" --attempt "$ATT" --job "$JOB" --effect "$EFF" --nonce "$NONCE")

cat > "$TMP/bin/herdr" <<'STUB'
#!/usr/bin/env bash
[[ "$1 $2" == "agent list" ]] && { printf '{"result":{"agents":[]}}\n'; exit 0; }
exit 0
STUB
cat > "$TMP/bin/tandem" <<'STUB'
#!/usr/bin/env bash
case "$1" in
  paths) shift; while (( $# )); do [[ "$1" == --root ]] && { printf '%s/.tandem-relay\n' "$2"; exit 0; }; shift; done; exit 1 ;;
  build) [[ "$2 $3" == "event new" || "$2" == roles ]] && exec "$TANDEM_REAL" "$@" ;;
  relay|state) exec "$TANDEM_REAL" "$@" ;;
esac
exit 0
STUB
chmod +x "$TMP/bin"/*

fixture() { # fixture <dir> <review_protocol or "">: a git repo whose relay carries a durable run
  local r="$1" proto="$2" relay="$1/.tandem-relay" extra=""
  rm -rf "$r"; mkdir -p "$r"; git -C "$r" init -q .
  mkdir -p "$relay/durable/$RUN"/{q,payloads,receipts,quarantine}
  [[ -n "$proto" ]] && extra=",\"review_protocol\":$proto"
  printf '{"schema_version":2,"run_id":"%s"%s,"tasks":[{"ordinal":1,"id":"receipt-store"},{"ordinal":2,"id":"second-task"}]}\n' "$RUN" "$extra" > "$relay/durable/$RUN/manifest.json"
  printf '{"engine_version":2,"protocol_version":2,"run_id":"%s","project_id":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}\n' "$RUN" > "$relay/active-run.json"
}
hb() { # hb <cwd> <args...>; ENVX holds extra NAME=value pairs
  local cwd="$1"; shift
  OUT=$(cd "$cwd" && env -i PATH="$TMP/bin:/usr/bin:/bin" HOME="$HOME" TANDEM_REAL="$TMP/tandem-real" \
          ${ENVX[@]+"${ENVX[@]}"} "$HANDBACK" "$@" 2>&1); RC=$?
}
queued() { ls "$1"/q/*.json 2>/dev/null | head -1; }
clear_q() { rm -f "$1"/q/* "$1"/payloads/*; }
RESULT="$TMP/result.json"; printf '{"schema_version":1,"status":"passed","findings":[]}\n' > "$RESULT"
ENVX=()

printf '== a run that opted in takes version 3 ==\n'
R="$TMP/opted"; fixture "$R" 3; DIR="$R/.tandem-relay/durable/$RUN"
hb "$R" --task 1 "${D[@]}" --review-started
[ "$RC" -eq 0 ] && ok "--review-started exits 0" || nope "--review-started exits 0 (rc $RC): $OUT"
Q=$(queued "$DIR")
if [ -n "$Q" ] && jq -e --arg a "$ATT" --arg e "$EFF" --arg n "$NONCE" '.schema_version==3 and .kind=="review_started" and .task_id=="receipt-store" and .attempt_id==$a and .effect_id==$e and .nonce==$n' "$Q" >/dev/null; then
  ok "it queues a version 3 review_started bound to the prompt's effect and nonce"
else nope "review_started envelope ($(cat "$Q" 2>/dev/null))"; fi
[ "$(tr -d '[:space:]' < "$DIR/$(jq -r .payload_path "$Q")")" = "{}" ] && ok "a start carries no more than an optional session" || nope "start payload"

clear_q "$DIR"
hb "$R" --task 1 "${D[@]}" --review-result "$RESULT"
[ "$RC" -eq 0 ] && ok "--review-result FILE exits 0" || nope "--review-result exits 0 (rc $RC): $OUT"
Q=$(queued "$DIR")
jq -e '.schema_version==3 and .kind=="review_result"' "$Q" >/dev/null 2>&1 && ok "it queues a version 3 review_result" || nope "review_result envelope ($(cat "$Q" 2>/dev/null))"
cmp -s "$RESULT" "$DIR/$(jq -r .payload_path "$Q")" && ok "the result travels as the exact bytes of the file" || nope "result bytes differ"
has "it tells the reviewer to stop" "$OUT" "Stop here"

clear_q "$DIR"
hb "$R" --task 1 "${D[@]}" --started
Q=$(queued "$DIR")
jq -e '.schema_version==2 and .kind=="builder_started"' "$Q" >/dev/null 2>&1 && ok "a builder on the same run still speaks version 2" || nope "builder version ($(cat "$Q" 2>/dev/null))"

printf '\n== a run that did not opt in is untouched, whatever the environment says ==\n'
O="$TMP/old"; fixture "$O" ""; ODIR="$O/.tandem-relay/durable/$RUN"
ENVX=(TANDEM_BUILD_ENGINE=durable TANDEM_REVIEW_DELIVERY=mailbox)
hb "$O" --task 1 "${D[@]}" --review-started
[ "$RC" -ne 0 ] && ok "review_started is refused" || nope "review_started was accepted"
[ -z "$(queued "$ODIR")" ] && ok "and nothing was queued" || nope "a refused start queued something"
hb "$O" --task 1 "${D[@]}" --review-result "$RESULT"
Q=$(queued "$ODIR")
jq -e '.schema_version==2' "$Q" >/dev/null 2>&1 && ok "a review_result keeps its version 2 form" || nope "old run version ($(cat "$Q" 2>/dev/null))"
ENVX=()

printf '\n== malformed calls queue nothing ==\n'
clear_q "$DIR"
EMPTY="$TMP/empty.json"; : > "$EMPTY"
for c in "both flags:--review-started --review-result $RESULT" "no such file:--review-result $TMP/absent.json" "an empty file:--review-result $EMPTY" \
         "with --started:--review-started --started" "with --report:--review-result $RESULT --report $RESULT" "with --stuck:--review-started --stuck"; do
  # shellcheck disable=SC2086
  hb "$R" --task 1 "${D[@]}" ${c#*:}
  [ "$RC" -ne 0 ] && [ -z "$(queued "$DIR")" ] && ok "${c%%:*} is refused" || nope "${c%%:*} (rc $RC): $OUT"
done
hb "$R" "${D[@]}" --review-started
[ "$RC" -ne 0 ] && has "a review message needs --task" "$OUT" "--task" || nope "a review message without --task was accepted"
hb "$R" --task 1 --run bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb --attempt "$ATT" --job "$JOB" --effect "$EFF" --nonce "$NONCE" --review-started
[ "$RC" -ne 0 ] && [ -z "$(queued "$DIR")" ] && ok "another run's identity is refused" || nope "a wrong run was accepted"
hb "$R" --task 1 --review-started
[ "$RC" -ne 0 ] && ok "a missing identity is refused" || nope "an incomplete identity was accepted"
hb "$R" --task 1 "${D[@]}" --review-result "$RESULT" --dry-run
[ "$RC" -eq 0 ] && [ -z "$(queued "$DIR")" ] && [ -z "$(ls -A "$DIR/payloads")" ] && has "--dry-run names the kind and queues nothing" "$OUT" "review_result" || nope "--dry-run (rc $RC): $OUT"

printf '\n== no durable run, no review message ==\n'
L="$TMP/legacy"; rm -rf "$L"; mkdir -p "$L/.tandem-relay/build"; git -C "$L" init -q .
hb "$L" --task 1 "${D[@]}" --review-started
[ "$RC" -ne 0 ] && has "it names the missing durable run" "$OUT" "durable run" || nope "a review message was accepted without a durable run"

printf '\n%s passed, %s failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

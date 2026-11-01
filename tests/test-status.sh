#!/usr/bin/env bash
# tandem-status must report every combination of pairing, review state,
# mailbox activity and bridge liveness, including a builder paired from a
# subdirectory, where the relay and identity mailboxes differ.
#
# Hermetic: tandem and herdr are stubbed. The stubs echo back the --root they
# receive, as the real commands do. jq fails: tandem-status asks tandem, so any
# jq call is a bug. This file's own fixtures use the real jq.
set -uo pipefail
. "$(cd "$(dirname "$0")" && pwd)/lib/records.sh"
STATUS="${1:-$(cd "$(dirname "$0")/.." && pwd)/bin/tandem-status}"
[ -x "$STATUS" ] || { echo "not executable: $STATUS" >&2; exit 2; }

PASS=0; FAIL=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }
chk()  { case "$OUT" in *"$2"*) ok "$1" ;; *) nope "$1 (missing: $2)" ;; esac; }
nchk() { case "$OUT" in *"$2"*) nope "$1 (unexpected: $2)" ;; *) ok "$1" ;; esac; }

TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
mkdir -p "$TMP/bin" "$TMP/repo/docs/plans" "$TMP/repo/.tandem-relay/q" "$TMP/repo/.tandem-relay/reports"
REPO="$TMP/repo"
git -C "$REPO" init -q
PLAN="$REPO/docs/plans/p.md"; printf '# plan\n' > "$PLAN"

# The real binary answers `reviewer show`.
export REAL_TANDEM; REAL_TANDEM=$(command -v tandem) || { echo "need an installed tandem" >&2; exit 2; }
cat > "$TMP/bin/tandem" <<'STUB'
#!/usr/bin/env bash
case "$1 $2" in
  "reviewer show"|"config builder"|"state engine") exec "$REAL_TANDEM" "$@" ;;
  "config project")
    root=""; shift 2
    while [ $# -gt 0 ]; do
      case "$1" in --root) root="$2"; shift 2 ;; *) shift ;; esac
    done
    printf '%s\0docs/plans\0' "$root"
    for _ in 1 2 3 4 5 6 7 8 9 10 11; do printf '\0'; done
    printf '%s\0' "${INVALID_MANIFEST:-}"
    exit 0 ;;
  "agent snapshot")
    [ "$SNAP_MODE" = unreachable ] && exit 3
    [ "$SNAP_MODE" = not-found ] && exit 10
    printf '%%1\t%s\tidle\n' "${SNAP_CWD:-$REPO}"; exit 0 ;;
  "gate latest-plan")
    [ -n "${LATEST_PLAN:-}" ] && printf '%s\n' "$LATEST_PLAN"; exit 0 ;;
  "gate review")
    case "$GATE_MODE" in
      clean)      printf '20260918-000001-1-1\n'; exit 0 ;;
      absent)     printf 'absent\n'; exit 24 ;;
      objections) printf 'objections\n20260918-000002-1-1\n'; exit 24 ;;
      no_reviewer|builder_review|reviewer) printf '%s\n' "$GATE_MODE"; exit 24 ;;
    esac
    ;;
esac
if [ "$1" = "paths" ] && [ "$2" = "relay" ]; then
  shift 2
  root=""
  while [ $# -gt 0 ]; do
    case "$1" in --root) root="$2"; shift 2 ;; *) shift ;; esac
  done
  printf '%s/.tandem-relay\n' "$root"
  exit 0
fi
exit 0
STUB
chmod +x "$TMP/bin/tandem"

rm -f "$TMP/bin/jq"
printf '#!/bin/sh\necho "jq was called: $*" >&2\nexit 99\n' > "$TMP/bin/jq"
chmod +x "$TMP/bin/jq"
printf '#!/usr/bin/env bash\nexit 0\n' > "$TMP/bin/herdr"
chmod +x "$TMP/bin/herdr"

# A sandbox can deny the process table while allowing kill -0, so ps is
# stubbed and kill -0 still decides liveness.
cat > "$TMP/bin/ps" <<'STUB'
#!/usr/bin/env bash
[ "${1:-}" = -p ] && [ "${2:-}" -eq "${2:-x}" ] 2>/dev/null \
  && [ "${3:-}" = -o ] && [ "${4:-}" = command= ] || exit 2
printf 'tandem-bridge\n'
STUB
chmod +x "$TMP/bin/ps"

write_builder "$REPO/.tandem-relay"

# Run from $REPO: the not-paired branch uses the git toplevel of the cwd.
run() { OUT=$(cd "$REPO" && PATH="$TMP/bin:$PATH" HERDR_ENV=1 REPO="$REPO" TANDEM_BUILD_ENGINE= \
                LATEST_PLAN="${LATEST_PLAN:-}" SNAP_MODE="${SNAP_MODE:-ok}" GATE_MODE="${GATE_MODE:-clean}" SNAP_CWD="${SNAP_CWD:-}" INVALID_MANIFEST="${INVALID_MANIFEST:-}" \
                "$STATUS" "$@" 2>&1); RC=$?; }

# --- paired, clean review, at the repo root -----------------------------------
SNAP_MODE=ok GATE_MODE=clean run
[ "$RC" -eq 0 ] && ok "paired+clean exits 0" || nope "paired+clean exits 0 (got $RC)"
chk "it shows the builder identity" "codex/auto"
chk "it shows the pane status"      "status: idle"
chk "it shows a clean review"       "clean (id 20260918-000001-1-1)"
chk "it names the plan"             "docs/plans/p.md"
chk "it shows zero mailbox activity" "0 handback report(s), 0 question(s)"
chk "it shows the bridge not running" "bridge   not running"
chk "it shows the engine and why" "engine   legacy (the default)"
nchk "it calls no jq" "jq was called"
nchk "it does not claim a subdirectory pairing at the repo root" "paired from a subdirectory"

# --- the reviewer ---
# With no record, say so and what it means.
chk "with no reviewer.json it says none is recorded" "reviewer none recorded"
chk "and that review and build will refuse"          "Review and build refuse until tandem-pair starts one."
write_reviewer "$REPO/.tandem-relay" codex reviewer gpt-6-astra
SNAP_MODE=ok GATE_MODE=clean run
chk  "with a record it names the reviewer"  "reviewer reviewer"
chk  "and its kind"                         "kind: codex"
chk  "and its model"                        "model: gpt-6-astra"
nchk "and no longer says none is recorded"  "none recorded"
write_reviewer "$REPO/.tandem-relay" claude
SNAP_MODE=ok GATE_MODE=clean run
chk  "an unmeasured model is not shown as a fact" "model: not recorded"
rm -f "$REPO/.tandem-relay/reviewer.json"

# --- paired, objections stand -------------------------------------------------
SNAP_MODE=ok GATE_MODE=objections run
[ "$RC" -eq 0 ] && ok "paired+objections still exits 0 -- this is a report, not a gate" \
                 || nope "paired+objections exits 0 (got $RC)"
chk "it shows objections stand" "objections stand"
chk "it names the review id"    "objections stand (id 20260918-000002-1-1). Fix the plan, or run tandem-handoff --override-review."

# --- paired, no review yet ----------------------------------------------------
SNAP_MODE=ok GATE_MODE=absent run
chk "it shows no review yet" "review   no review yet. Run tandem-review."
nchk "with no id it prints no id" "(id"

# --- the reviewer gate reasons are explained, never printed raw ---------------
SNAP_MODE=ok GATE_MODE=no_reviewer run
chk "no_reviewer says the pair has no reviewer" "no reviewer is recorded"
chk "no_reviewer names the block remedy" 'a "reviewer" block in .tandem.json'
chk "no_reviewer names the flag remedy" "tandem-pair --reviewer KIND"
SNAP_MODE=ok GATE_MODE=builder_review run
chk "builder_review says the builder wrote the review" "the builder wrote this review"
chk "builder_review says to re-review" "Run tandem-review again so the reviewer pane reviews."
case "$OUT" in *"review   builder_review"*) nope "builder_review is printed raw" ;; *) ok "builder_review is not printed raw" ;; esac
SNAP_MODE=ok GATE_MODE=reviewer run
chk "reviewer says the reviewer changed" "the reviewer's kind or model changed since the review"
chk "reviewer says to re-review" "Run tandem-review again."

# --- mailbox activity: questions queued, reports waiting ---------------------
echo '{}' > "$REPO/.tandem-relay/q/1.json"
echo '{}' > "$REPO/.tandem-relay/q/2.json"
echo 'report' > "$REPO/.tandem-relay/reports/r1.md"
SNAP_MODE=ok GATE_MODE=clean run
chk "it counts handback reports" "1 handback report(s)"
chk "it counts queued questions" "2 question(s) queued unanswered"
rm -f "$REPO/.tandem-relay/q"/*.json "$REPO/.tandem-relay/reports"/*.md

# --- bridge liveness: a real running process named tandem-bridge -------------
cat > "$TMP/tandem-bridge" <<'BRIDGESTUB'
#!/usr/bin/env bash
sleep 60
BRIDGESTUB
chmod +x "$TMP/tandem-bridge"
"$TMP/tandem-bridge" & BRIDGE_PID=$!
echo "$BRIDGE_PID" > "$REPO/.tandem-relay/bridge.pid"
SNAP_MODE=ok GATE_MODE=clean run
chk "it reports the bridge running" "bridge   running (pid $BRIDGE_PID)"
kill "$BRIDGE_PID" 2>/dev/null; wait "$BRIDGE_PID" 2>/dev/null

# --- bridge liveness: a stale pid file (process no longer exists) ------------
SNAP_MODE=ok GATE_MODE=clean run
chk "it reports a stale bridge pid as not running" "bridge   not running. Its pid file is left from an earlier run."
rm -f "$REPO/.tandem-relay/bridge.pid"

# --- paired from a subdirectory: the two mailboxes differ ---
# builder.json is only in the subdirectory's mailbox; activity is only in the
# toplevel's.
SUB="$REPO/sub"; mkdir -p "$SUB/.tandem-relay"
jq -n --arg k claude --arg a ask '{kind:$k, autonomy:$a, adopted:true}' > "$SUB/.tandem-relay/builder.json"
echo '{}' > "$REPO/.tandem-relay/q/1.json"
echo 'report' > "$REPO/.tandem-relay/reports/r1.md"
SNAP_MODE=ok GATE_MODE=clean SNAP_CWD="$SUB" run
chk "it reads identity from the subdirectory's own mailbox" "claude/ask"
chk "it names the divergent identity mailbox" "paired from a subdirectory, so its identity is in $SUB/.tandem-relay"
chk "it says an adopted builder's settings are not enforced" "(adopted, so tandem cannot enforce its launch settings)"
chk "it still counts mailbox activity from the walked repo-root relay" "1 handback report(s), 1 question(s)"
nchk "it does not show the wrong builder identity" "codex/auto"
rm -rf "$SUB"
rm -f "$REPO/.tandem-relay/q"/*.json "$REPO/.tandem-relay/reports"/*.md

# --- a builder.json tandem cannot read ---
# On main a builder.json that was not JSON stopped the script with jq's error.
printf '{not json' > "$REPO/.tandem-relay/builder.json"
SNAP_MODE=ok GATE_MODE=clean run
[ "$RC" -eq 0 ] && ok "builder.json that is not JSON still exits 0" || nope "builder.json that is not JSON exits 0 (got $RC)"
chk "builder.json that is not JSON prints no recorded identity" "(no recorded identity)"
printf '{"kind":"codex","autonomy":"auto","adopted":"yes"}' > "$REPO/.tandem-relay/builder.json"
SNAP_MODE=ok GATE_MODE=clean run
[ "$RC" -eq 0 ] && ok "adopted as a string still exits 0" || nope "adopted as a string exits 0 (got $RC)"
nchk "adopted as a string is not adopted" "adopted,"
write_builder "$REPO/.tandem-relay"

# --- a manifest that is not a JSON object ---
SNAP_MODE=ok GATE_MODE=clean INVALID_MANIFEST="$REPO/.tandem.json" run
chk "an invalid manifest warns" "tandem-status: $REPO/.tandem.json is not a JSON object, so its settings were ignored."

# --- a real stored review is found through a symlinked builder cwd ---
# The stubbed gate ignores --plan and --root, so this uses the real tandem for
# `gate review` and `paths relay` and stubs only `agent snapshot`. The gate
# matches a review's plan path exactly.
REAL_TANDEM=$(command -v tandem || true)
if [ -z "$REAL_TANDEM" ]; then
  echo "  skip real-review-through-symlink: tandem not installed"
else
  RTMP=$(mktemp -d)
  REALREPO="$RTMP/realrepo"
  mkdir -p "$REALREPO/docs/plans" "$REALREPO/.tandem-relay/reviews"
  git -C "$REALREPO" init -q
  git -C "$REALREPO" config user.email t@example.com
  git -C "$REALREPO" config user.name test
  RPLAN="$REALREPO/docs/plans/p.md"; printf '# plan\n' > "$RPLAN"
  git -C "$REALREPO" add -A && git -C "$REALREPO" commit -qm init

  # The builder's cwd is the symlinked spelling, as tandem-pair records it.
  LINKREPO="$RTMP/linkrepo"; ln -s "$REALREPO" "$LINKREPO"
  LOGICAL_PLAN="$LINKREPO/docs/plans/p.md"

  jq -n --arg k codex --arg a auto '{kind:$k, autonomy:$a, adopted:false}' \
    > "$REALREPO/.tandem-relay/builder.json"
  jq -n '{kind:"claude", name:"reviewer", model:"opus", autonomy:"auto", model_measured:true}' \
    > "$REALREPO/.tandem-relay/reviewer.json"
  cp "$RPLAN" "$REALREPO/.tandem-relay/reviews/20260918-000001-1-1.plan.md"
  printf 'review body\n' > "$REALREPO/.tandem-relay/reviews/20260918-000001-1-1.md"
  jq -n --arg plan "$LOGICAL_PLAN" \
     '{plan:$plan, verdict:"clean", kind:"codex", autonomy:"auto",
       reviewed_by:"reviewer", reviewer_kind:"claude", reviewer_model:"opus",
       reviewer_model_measured:true, override_reason:""}' \
    > "$REALREPO/.tandem-relay/reviews/20260918-000001-1-1.json"

  mkdir -p "$RTMP/bin"
  cat > "$RTMP/bin/tandem" <<REALSTUB
#!/usr/bin/env bash
if [ "\$1 \$2" = "agent snapshot" ]; then
  printf '%%1\t%s\tidle\n' "$LINKREPO"; exit 0
fi
exec "$REAL_TANDEM" "\$@"
REALSTUB
  chmod +x "$RTMP/bin/tandem"
  printf '#!/bin/sh\necho "jq was called: $*" >&2\nexit 99\n' > "$RTMP/bin/jq"; chmod +x "$RTMP/bin/jq"
  printf '#!/usr/bin/env bash\nexit 0\n' > "$RTMP/bin/herdr"; chmod +x "$RTMP/bin/herdr"

  # An isolated PATH with this scenario's own stubs.
  OUT=$(PATH="$RTMP/bin:$PATH" HERDR_ENV=1 "$STATUS" 2>&1); RC=$?
  [ "$RC" -eq 0 ] && ok "real review through a symlinked checkout exits 0" \
                   || nope "real review through a symlinked checkout exits 0 (got $RC)"
  chk "it finds the real clean review despite the symlink" "clean (id 20260918-000001-1-1)"
  nchk "it does not report a false subdirectory divergence" "paired from a subdirectory"

  rm -rf "$RTMP"
fi

# --- a plan reviewed by explicit path outside the plans directory -------------
# Bare tandem-status must follow the review, not the newest file in plans_dir.
OUTSIDE="$TMP/outside-plan.md"; printf '# plan\n' > "$OUTSIDE"
SNAP_MODE=ok GATE_MODE=clean LATEST_PLAN="$OUTSIDE" run
chk  "it names the plan reviewed outside the plans directory" "$OUTSIDE"
nchk "it does not fall back to the newest file in plans_dir"  "docs/plans/p.md"

# --- not paired: no agent named 'builder' -------------------------------------
SNAP_MODE=not-found GATE_MODE=absent run
[ "$RC" -eq 0 ] && ok "not paired still exits 0" || nope "not paired exits 0 (got $RC)"
chk "it says not paired" "builder  not paired. Run tandem-pair to start one."
chk "it still resolves the plan" "docs/plans/p.md"

# --- Herdr socket unreachable ---
SNAP_MODE=unreachable GATE_MODE=clean run
[ "$RC" -ne 0 ] && ok "socket unreachable is a real failure" || nope "socket unreachable exits 0"
chk "it names the socket as unreachable" "Herdr socket is unreachable"

# --- outside a Herdr pane entirely --------------------------------------------
OUT=$(env -u HERDR_ENV PATH="$TMP/bin:$PATH" "$STATUS" 2>&1); RC=$?
[ "$RC" -ne 0 ] && ok "refuses outside a Herdr pane" || nope "should refuse outside a Herdr pane"
chk "it names the missing HERDR_ENV" "HERDR_ENV"

printf '\nstatus: passed %d, failed %d\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

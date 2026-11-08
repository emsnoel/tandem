#!/usr/bin/env bash
# What a legacy build left behind after the upgrade, with the real binary.
# Each section is one row of the upgrade table: a leftover build.json refuses
# a new start and is never read or changed, an active run ignores it, an old
# task message is set aside, and old runs still open and archive.
#
# Herdr is faked. The repo path has a space, so every printed path is tested.
set -uo pipefail
. "$(cd "$(dirname "$0")" && pwd)/lib/records.sh"
ROOT_DIR=$(cd "$(dirname "$0")/.." && pwd)

PASS=0; FAIL=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }
has()  { case "$OUT" in *"$2"*) ok "$1" ;; *) nope "$1 (missing: $2 in: $OUT)" ;; esac; }

command -v jq >/dev/null || { echo "need jq" >&2; exit 2; }
TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
TMP=$(cd "$TMP" && pwd -P)
mkdir -p "$TMP/bin"
(cd "$ROOT_DIR" && go build -o "$TMP/tandem-real" ./cmd/tandem) || { echo "could not build tandem"; exit 1; }
. "$ROOT_DIR/tests/lib/fake-herdr.sh"
. "$ROOT_DIR/tests/lib/fake-socket.sh"
fake_herdr_install "$TMP/bin" "$TMP/fake"
FAKE="$TMP/fake" PATH="$TMP/bin:$PATH" fake_socket_start "$TMP" || exit 1
# Reading the panes and the review gate are not under test here.
cat > "$TMP/bin/tandem" <<'STUB'
#!/usr/bin/env bash
case "$1 $2" in
  "agent snapshot") [ -n "${PANE:-}" ] || exit 10; printf '%s\t%s\tidle\n' "$PANE" "$REPO"; exit 0 ;;
  "agent status")   [ -n "${PANE:-}" ] || exit 1; printf 'idle\n'; exit 0 ;;
  "config check-command") printf 'true\0'; exit 0 ;;
esac
case "$1" in gate|pane) exit 0 ;; esac
exec "$TANDEM_REAL" "$@"
STUB
printf '#!/usr/bin/env bash\nexit 0\n' > "$TMP/bin/tandem-review"
chmod +x "$TMP/bin/tandem" "$TMP/bin/tandem-review"

export TANDEM_REAL="$TMP/tandem-real" TANDEM_STATE_HOME="$TMP/state" FAKE="$TMP/fake" HERDR_ENV=1
PATHX="$TMP/bin:$ROOT_DIR/bin:/usr/bin:/bin"

mkrepo() { # mkrepo DIR: a committed repo with a plan and a paired relay
  rm -rf "$1"; mkdir -p "$1/.tandem-relay" "$1/.tandem-history/old"; git -C "$1" init -q
  git -C "$1" config user.email t@t; git -C "$1" config user.name t
  printf '.tandem-relay/\n.tandem-history/\n' > "$1/.gitignore"
  printf 'kept\n' > "$1/.tandem-history/old/build.json"
  write_builder "$1/.tandem-relay"; write_reviewer "$1/.tandem-relay" claude
  printf '# Plan\n\n**Goal:** g.\n\n### Task 1: First\n\nfirst\n' > "$1/plan.md"
  git -C "$1" add . && git -C "$1" commit -qm plan
}
tree() { (cd "$1" && find . -type f -print0 | sort -z | xargs -0 shasum -a 256 2>/dev/null); }
td() { env -u TANDEM_BUILD_ENGINE PATH="$PATHX" REPO="$R" "$@"; }
handoff() { OUT=$(cd "$R" && td PANE=w1:p1 tandem-handoff "$R/plan.md" "$@" 2>&1); RC=$?; }
leftover() { mkdir -p "$REC_DIR"; printf '%s' "$1" > "$REC"; touch -t 202601010101 "$REC"; }
stamp() { stat -f '%m %z' "$REC" 2>/dev/null || stat -c '%Y %s' "$REC"; }

R="$TMP/my repo"; REC_DIR="$R/.tandem-relay/build"; REC="$REC_DIR/build.json"

printf '\n== a leftover refuses a new start ==\n'
mkrepo "$R"; leftover '{"state":"running","id":"b1"}'
BEFORE_STAMP=$(stamp); BEFORE_SUM=$(shasum "$REC"); BEFORE_HIST=$(tree "$R/.tandem-history")
handoff
[ "$RC" -eq 23 ] && ok "the handoff exits 23" || nope "rc $RC: $OUT"
has "it says legacy is gone" "legacy engine is gone"
has "it names the file" "$REC"
has "it says how to finish the build" "install.sh --version v0.6.0"
has "and gives a remove command that survives a space" 'my\ repo/.tandem-relay/build/build.json'
has "and that nothing was sent" "Nothing was sent"
[ ! -f "$R/.tandem-relay/active-run.json" ] && ok "no run was created" || nope "a run was created"
[ "$(stamp)" = "$BEFORE_STAMP" ] && [ "$(shasum "$REC")" = "$BEFORE_SUM" ] && ok "the file's bytes and mtime are unchanged" || nope "the file changed"
OUT=$(td tandem build start --plan "$R/plan.md" --root "$R" --builder-kind codex 2>&1 >/dev/null); RC=$?
[ "$RC" -eq 23 ] && has "a direct start prints the one fixed line" "the legacy engine is gone" || nope "direct start rc $RC: $OUT"
[ "$(printf '%s\n' "$OUT" | wc -l | tr -d ' ')" = 1 ] && ok "and only one line" || nope "more than one line: $OUT"

printf '\n== the file is never parsed ==\n'
leftover 'not json at all {{{'
handoff
[ "$RC" -eq 23 ] && has "a garbage build.json gives the same refusal" "legacy engine is gone" || nope "garbage rc $RC: $OUT"

printf '\n== removing it allows a start ==\n'
rm -f "$REC"
handoff --dry-run
[ "$RC" -eq 0 ] && has "a dry run would start" "would start" || nope "after removal rc $RC: $OUT"

printf '\n== an active run ignores it ==\n'
OUT=$(td tandem build start --plan "$R/plan.md" --root "$R" --builder builder --planner planner --builder-kind codex --check true 2>&1); RC=$?
[ "$RC" -eq 0 ] && ok "a run starts" || nope "start rc $RC: $OUT"
leftover '{"state":"running"}'
fake_set_state idle
for verb in tick status; do
  OUT=$(td tandem build "$verb" --root "$R" 2>&1); RC=$?
  [ "$RC" -eq 0 ] && ok "build $verb works beside it" || nope "build $verb rc $RC: $OUT"
done
OUT=$(td tandem build stop --root "$R" 2>&1); RC=$?
[ "$RC" -eq 0 ] && ok "build stop works beside it" || nope "stop rc $RC: $OUT"

printf '\n== an old task message is set aside ==\n'
mkrepo "$R"; Q="$R/.tandem-relay/q"; mkdir -p "$Q"
printf '{"id":"x","kind":"task","from":"builder","question":"task","task":1}\n' > "$Q/x.json"
OUT=$(cd "$R" && td tandem-bridge --mailbox "$R/.tandem-relay" --once 2>&1); RC=$?
[ "$RC" -eq 0 ] && ok "the bridge keeps running" || nope "bridge rc $RC: $OUT"
[ -f "$R/.tandem-relay/quarantine-v1/x.json" ] && ok "the message is in quarantine-v1" || nope "not set aside: $OUT"
grep -q 'legacy is gone' "$R/.tandem-relay/quarantine-v1/x.json.reason" 2>/dev/null && ok "beside its reason" || nope "no reason file"

printf '\n== unpair closes a relay holding build.json ==\n'
mkrepo "$R"; leftover '{"state":"running"}'
BEFORE_HIST=$(tree "$R/.tandem-history")
OUT=$(cd "$R" && td tandem-unpair --skip-checks 2>&1); RC=$?
[ "$RC" -eq 0 ] && ok "unpair exits 0" || nope "unpair rc $RC: $OUT"
[ ! -e "$R/.tandem-relay" ] && ok "the relay is gone" || nope "the relay is still there"
[ "$(tree "$R/.tandem-history")" = "$BEFORE_HIST" ] && ok ".tandem-history is untouched" || nope ".tandem-history changed"

printf '\n== an old run survives ==\n'
# A repo and state home written by the last binary that still had legacy: one
# paused run, one archived. Unpacked here, so its paths are rewritten.
FX="$TMP/fx"; mkdir -p "$FX"
tar -xzf "$ROOT_DIR/tests/testdata/old-runs.tar.gz" -C "$FX"
sed -i.bak "s|/FIXTURE-ROOT|$FX|g" "$FX/state/registry.json"; rm -f "$FX/state/registry.json.bak"
fxd() { env -u TANDEM_BUILD_ENGINE PATH="$PATHX" REPO="$FX/repo" TANDEM_STATE_HOME="$FX/state" "$@"; }
OUT=$(fxd tandem build status --root "$FX/repo" --json 2>&1); RC=$?
[ "$RC" -eq 0 ] && [ "$(printf '%s' "$OUT" | jq -r .state)" = paused ] && ok "the paused run reports status" || nope "status rc $RC: $OUT"
PAUSED=$(printf '%s' "$OUT" | jq -r .run_id)
OUT=$(fxd tandem state status --json --root "$FX/repo" 2>&1); RC=$?
[ "$RC" -eq 0 ] && [ "$(printf '%s' "$OUT" | jq -r .readable)" = true ] && ok "the store opens with both runs" || nope "state status rc $RC: $OUT"
OUT=$(fxd tandem state archive --run "$PAUSED" --root "$FX/repo" 2>&1); RC=$?
[ "$RC" -eq 0 ] && ok "the paused run archives" || nope "archive rc $RC: $OUT"

printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

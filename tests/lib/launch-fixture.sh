# launch-fixture.sh: shared setup for the tandem-pair launch suites
# (test-launch-flags.sh, test-launch-adoption.sh). Source it after
# setting ROOT. It builds tandem into $TMP/bin, stubs herdr as a
# planner-only session and defines run_pair and the chk helpers.
[ -x "$PAIR" ] || { echo "not executable: $PAIR" >&2; exit 2; }

PASS=0; FAIL=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }
chk()  { case "$OUT" in *"$2"*) ok "$1" ;; *) nope "$1 (missing: $2)" ;; esac; }
nchk() { case "$OUT" in *"$2"*) nope "$1 (unexpected: $2)" ;; *) ok "$1" ;; esac; }

TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
mkdir -p "$TMP/bin" "$TMP/codex" "$TMP/copilot" "$TMP/repo" "$TMP/home" "$TMP/nohome"
REPO_PHYS=$(cd "$TMP/repo" && pwd -P)
printf '{"projects":{"%s":{"hasTrustDialogAccepted":true}}}\n' "$REPO_PHYS" \
  > "$TMP/home/.claude.json"

# Build from this tree into $TMP/bin; test-staleness.sh checks the installed
# binary separately.
GOCACHE="${GOCACHE_OVERRIDE:-$ROOT/.gocache}" GOMODCACHE="${GOMODCACHE_OVERRIDE:-$ROOT/.gocache/mod}" \
  go -C "$ROOT" build -o "$TMP/bin/tandem" ./cmd/tandem \
  || { echo "could not build tandem for the fixture" >&2; exit 2; }

# A planner-only session: one claude agent in a wide pane, so the split
# direction is fixed.
cat > "$TMP/bin/herdr" <<'STUB'
#!/usr/bin/env bash
case "$1 $2" in
  # agent_status is required by the facade and always present in real output.
  "agent list")
    # FAKE_RUNNING="name:kind ..." adds already-running agents beside the planner.
    printf '{"result":{"agents":[{"pane_id":"p1","agent":"claude","name":null,"cwd":"%s","agent_status":"idle"}' "$FAKE_CWD"
    for _a in ${FAKE_RUNNING:-}; do
      printf ',{"pane_id":"r-%s","agent":"%s","name":"%s","cwd":"%s","agent_status":"idle"}' "${_a%%:*}" "${_a##*:}" "${_a%%:*}" "$FAKE_CWD"
    done
    printf ']}}' ;;
  "pane layout") printf '{"result":{"layout":{"panes":[{"pane_id":"p1","rect":{"width":200,"height":50}}]}}}' ;;
  *) printf '{"error":{"code":"unexpected","message":"stub got: %s"}}' "$*" >&2; exit 1 ;;
esac
STUB
chmod +x "$TMP/bin/herdr"

# run_pair <env command...> [-- <tandem-pair flags...>]
run_pair() {
  local pre=()
  while (( $# )) && [[ "$1" != -- ]]; do pre+=("$1"); shift; done
  (( $# )) && shift
  OUT=$(PATH="$TMP/bin:$PATH" HERDR_ENV=1 HERDR_PANE_ID=p1 \
    HOME="$TMP/home" CODEX_HOME="$TMP/codex" COPILOT_HOME="$TMP/copilot" \
    FAKE_CWD="$TMP/repo" \
    "${pre[@]}" "$PAIR" --dry-run --skip-doctor --cwd "$TMP/repo" ${1+"$@"} 2>&1); RC=$?
}

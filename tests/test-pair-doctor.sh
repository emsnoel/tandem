#!/usr/bin/env bash
# tandem-pair runs `tandem doctor` before it opens any pane, stops with
# doctor's exit code when doctor fails, and skips it with --skip-doctor.
#
# Hermetic: a stub herdr, --dry-run, and a stub `tandem doctor` in front of the
# real binary, so no pane is touched and the result does not depend on this
# machine's setup.
set -uo pipefail
PAIR="${1:-$(cd "$(dirname "$0")/.." && pwd)/bin/tandem-pair}"
[ -x "$PAIR" ] || { echo "not executable: $PAIR" >&2; exit 2; }

PASS=0; FAIL=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }
SHOWN=0
chk()  { case "$2" in *"$3"*) ok "$1" ;; *) nope "$1 (missing: $3)"
           [ "$SHOWN" -eq 1 ] || { SHOWN=1; printf '%s\n' "$2" | sed 's/^/       | /'; } ;; esac; }
lack() { case "$2" in *"$3"*) nope "$1 (found: $3)" ;; *) ok "$1" ;; esac; }

command -v jq >/dev/null || { echo "need jq" >&2; exit 2; }
REAL_TANDEM=$(command -v tandem) || { echo "need tandem on PATH" >&2; exit 2; }
export REAL_TANDEM

TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
mkdir -p "$TMP/bin" "$TMP/doctor-repo"
ln -s "$(command -v jq)" "$TMP/bin/jq"
cat > "$TMP/bin/herdr" <<'STUB'
#!/usr/bin/env bash
case "$1 $2" in
  "agent list")
    printf '{"result":{"agents":[{"pane_id":"w1:p1","agent":"claude","name":"","agent_status":"idle","cwd":"%s","tab_id":"w1:t1"}]}}\n' "$REPO" ;;
  "tab get")
    printf '{"result":{"tab":{"label":"mine","number":3,"tab_id":"w1:t1"}}}\n' ;;
  *)
    printf '{"error":{"code":"agent_not_found","message":"agent target %s not found"}}\n' "${3:-}" >&2
    exit 1 ;;
esac
STUB
# Records each doctor call and answers with $DOCTOR_RC. Everything else goes to
# the real binary.
cat > "$TMP/bin/tandem" <<'STUB'
#!/usr/bin/env bash
if [ "${1:-} ${2:-}" = "agent prelaunch" ]; then
  printf '%s\n' "$*" >> "$DOCTOR_LOG.prelaunch"
fi
if [ "${1:-}" = doctor ]; then
  printf '%s\n' "$*" >> "$DOCTOR_LOG"
  [ -z "${DOCTOR_OUT-DOCTOR-OUTPUT}" ] || echo "${DOCTOR_OUT-DOCTOR-OUTPUT}"
  exit "$DOCTOR_RC"
fi
exec "$REAL_TANDEM" "$@"
STUB
chmod +x "$TMP/bin/herdr" "$TMP/bin/tandem"

REPO=$(cd "$TMP/doctor-repo" && pwd -P); export REPO
git -C "$REPO" init -q
DOCTOR_LOG="$TMP/doctor.log"; export DOCTOR_LOG

# pair RC ARGS...: run tandem-pair with doctor answering RC and printing
# $DOCTOR_OUT (DOCTOR-OUTPUT when unset). Sets OUT and PAIR_RC.
pair() {
  local rc="$1"; shift
  : > "$DOCTOR_LOG"; : > "$DOCTOR_LOG.prelaunch"
  PAIR_RC=0
  OUT=$(cd "$REPO" && env PATH="$TMP/bin:$PATH" DOCTOR_RC="$rc" ${DOCTOR_OUT+DOCTOR_OUT="$DOCTOR_OUT"} HERDR_ENV=1 HERDR_PANE_ID=w1:p1 \
    HERDR_SKIP_HOOK_CHECK=1 "$PAIR" --dry-run "$@" 2>&1) || PAIR_RC=$?
}

pair 30 --builder claude --reviewer claude
[ "$PAIR_RC" -eq 30 ] && ok "a failed doctor stops pairing with its exit code" \
  || nope "a failed doctor stops pairing with its exit code (got $PAIR_RC)"
chk "doctor's report is shown" "$OUT" "DOCTOR-OUTPUT"
chk "the refusal names the way out" "$OUT" "--skip-doctor"
lack "no pane is split when doctor fails" "$OUT" "pane split"

pair 0 --builder claude --reviewer claude
chk "a passing doctor lets pairing continue" "$OUT" "pane split"
chk "doctor gets the builder kind" "$(cat "$DOCTOR_LOG")" "--builder claude"
chk "doctor gets the reviewer kind" "$(cat "$DOCTOR_LOG")" "--reviewer claude"
chk "doctor checks the folder the agents start in" "$(cat "$DOCTOR_LOG")" "--cwd $REPO"
chk "prelaunch hears the builder kind the user named" "$(cat "$DOCTOR_LOG.prelaunch")" "--role builder --kind claude --root"
chk "prelaunch gets --user-kind for the builder" "$(grep -- '--role builder' "$DOCTOR_LOG.prelaunch")" "--user-kind claude"
chk "prelaunch gets --user-kind for the reviewer" "$(grep -- '--role reviewer' "$DOCTOR_LOG.prelaunch")" "--user-kind claude"

pair 0 --builder claude --builder-model m1 --builder-effort high --builder-autonomy auto \
  --reviewer claude --reviewer-model m2 --reviewer-effort low
for f in "--builder-model m1" "--builder-effort high" "--builder-autonomy auto" \
         "--reviewer-model m2" "--reviewer-effort low"; do
  chk "doctor gets $f" "$(cat "$DOCTOR_LOG")" "$f"
done

DOCTOR_OUT="WARN-LINE" pair 0 --builder claude --reviewer claude
chk "a passing doctor's warnings are shown" "$OUT" "WARN-LINE"
chk "tandem-pair asks doctor for problems only" "$(cat "$DOCTOR_LOG")" "--problems"
DOCTOR_OUT="" pair 0 --builder claude --reviewer claude
lack "a clean doctor prints nothing" "$OUT" "DOCTOR-OUTPUT"
chk "a clean doctor lets pairing continue" "$OUT" "pane split"

pair 30 --skip-doctor --builder claude --reviewer claude
[ -s "$DOCTOR_LOG" ] && nope "--skip-doctor does not run doctor" || ok "--skip-doctor does not run doctor"
chk "--skip-doctor pairs anyway" "$OUT" "pane split"

printf '\npair-doctor: passed %d, failed %d\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

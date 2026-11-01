#!/usr/bin/env bash
# tandem-init writes .tandem.json from detected settings and adds the relay
# mailbox and Q&A log to .gitignore.
#
# Hermetic: tandem is stubbed, apart from `config write-manifest`, which is the
# real command. jq fails: tandem-init asks tandem, so any jq call is a bug. The
# tests read the manifest with the real jq.
set -uo pipefail
INIT_BIN="${1:-$(cd "$(dirname "$0")/.." && pwd)/bin/tandem-init}"
[ -x "$INIT_BIN" ] || { echo "not executable: $INIT_BIN" >&2; exit 2; }

PASS=0; FAIL=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }
chk()  { case "$OUT" in *"$2"*) ok "$1" ;; *) nope "$1 (missing: $2)" ;; esac; }

command -v jq >/dev/null || { echo "need jq" >&2; exit 2; }

TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
mkdir -p "$TMP/bin"
rm -f "$TMP/bin/jq"
printf '#!/bin/sh\necho "jq was called: $*" >&2\nexit 99\n' > "$TMP/bin/jq"
(cd "$(dirname "$0")/.." && go build -o "$TMP/tandem-real" ./cmd/tandem) || { echo "could not build tandem" >&2; exit 2; }
export TANDEM_REAL="$TMP/tandem-real"

# `config project` answers the detected settings as fields, root echoed from
# --root. PROJECT_GAPS=1 leaves plans_dir, check_command and conventions_file
# empty; INVALID_MANIFEST fills field 14. `tandem paths` resolves relative to
# the given --root, like the real one.
cat > "$TMP/bin/tandem" <<'STUB'
#!/usr/bin/env bash
[[ "${TANDEM_RC:-0}" == 0 ]] || exit "${TANDEM_RC}"
case "$1 $2" in
  "config write-manifest") exec "$TANDEM_REAL" "$@" ;;
  "config project")
    root=""; shift 2
    while (( $# )); do [[ "$1" == --root ]] && root="$2"; shift; done
    if [[ "${PROJECT_GAPS:-0}" == 1 ]]; then
      printf '%s\0\0\0\0codex\0' "$root"
    else
      printf '%s\0docs/plans\0true\0AGENTS.md\0codex\0' "$root"
    fi
    printf '%s\0' "" 6000 200000 legacy "" "" "" detected "${INVALID_MANIFEST:-}" a b
    exit 0 ;;
esac
case "$1" in
  paths)
    shift
    sub="$1"; shift
    while (( $# )); do
      if [[ "$1" == --root ]]; then
        case "$sub" in
          manifest) printf '%s/.tandem.json\n' "$2" ;;
          relay)    printf '%s/.tandem-relay\n' "$2" ;;
        esac
        exit 0
      fi
      shift
    done
    exit 1 ;;
esac
exit 0
STUB
chmod +x "$TMP/bin"/*

run() { OUT=$(PATH="$TMP/bin:$PATH" "$@" 2>&1); RC=$?; }

# --- --help ------------------------------------------------------------------
run "$INIT_BIN" --help
[[ "$RC" == 0 ]] && ok "--help exits 0" || nope "--help exits 0 (got $RC)"
chk "--help prints usage" "usage: tandem-init"

# --- missing prerequisites -----------------------------------------------------
# The trimmed PATH holds bash and the failing jq, and leaves out tandem.
TRIMMED="$TMP/trimmed-bin"; mkdir -p "$TRIMMED"
ln -s "$TMP/bin/jq" "$TRIMMED/jq"
ln -s "$(command -v bash)" "$TRIMMED/bash"
OUT=$(PATH="$TRIMMED" "$INIT_BIN" --root "$TMP" 2>&1); RC=$?
[[ "$RC" == 1 ]] && ok "missing tandem on PATH exits 1" || nope "missing tandem on PATH exits 1 (got $RC)"
chk "missing tandem on PATH names it" "tandem not found on PATH"

# --- --root must be a directory ------------------------------------------------
run "$INIT_BIN" --root "$TMP/no-such-dir"
[[ "$RC" == 1 ]] && ok "non-directory --root exits 1" || nope "non-directory --root exits 1 (got $RC)"
chk "non-directory --root names the path" "not a directory"

# --- tandem paths manifest fails (binary too old) ---
REPO="$TMP/repo"; mkdir -p "$REPO"
OUT=$(PATH="$TMP/bin:$PATH" TANDEM_RC=1 "$INIT_BIN" --root "$REPO" 2>&1); RC=$?
[[ "$RC" == 1 ]] && ok "'tandem paths' failure exits 1" || nope "'tandem paths' failure exits 1 (got $RC)"
chk "'tandem paths' failure names the remedy" "re-run install.sh"

# --- writes .tandem.json in a non-git directory ---
REPO="$TMP/plain"; mkdir -p "$REPO"
run "$INIT_BIN" --root "$REPO"
[[ "$RC" == 0 ]] && ok "fresh, non-git repo: exits 0" || nope "fresh, non-git repo: exits 0 (got $RC)"
[[ -f "$REPO/.tandem.json" ]] && ok "it wrote .tandem.json" || nope "it wrote .tandem.json"
MANIFEST=$(cat "$REPO/.tandem.json" 2>/dev/null)
[[ "$(jq -r '.plans_dir' <<<"$MANIFEST")" == "docs/plans" ]] && ok "it recorded the detected plans_dir" || nope "it recorded the detected plans_dir"
[[ "$(jq -r '.check_command' <<<"$MANIFEST")" == "true" ]] && ok "it recorded the detected check_command" || nope "it recorded the detected check_command"
[[ "$(jq -r '.builder_kind' <<<"$MANIFEST")" == "codex" ]] && ok "it recorded the detected builder_kind" || nope "it recorded the detected builder_kind"
[[ "$(jq -r '.prompt_extra' <<<"$MANIFEST")" == "" ]] && ok "it left prompt_extra blank for the user to fill in" || nope "it left prompt_extra blank for the user to fill in"
chk "non-git repo skips .gitignore (relay)" "not a git repo, so .gitignore was left alone. The relay will use $REPO/.tandem-relay."
chk "non-git repo skips .gitignore (log)" "not a git repo, so .gitignore was left alone. The log will use $REPO/.tandem-log."
case "$OUT" in *"jq was called"*) nope "it calls no jq: $OUT" ;; *) ok "it calls no jq" ;; esac
chk "the listing shows check_paths as a list" "    check_paths: a, b"
chk "the listing shows an empty value as (none)" "    prompt_extra: (none)"
for k in root plans_dir check_command conventions_file builder_kind task_input_budget context_limit \
         build_engine review_delivery acceptance_policy collaboration source; do
  chk "the listing shows $k" "    $k: "
done
chk "the listing shows a number" "    task_input_budget: 6000"
[[ ! -f "$REPO/.gitignore" ]] && ok "non-git repo: no .gitignore was created" || nope "non-git repo: no .gitignore was created"

# --- does not overwrite an existing .tandem.json without --force -------------
REPO="$TMP/existing"; mkdir -p "$REPO"
printf '{"plans_dir":"custom"}\n' > "$REPO/.tandem.json"
run "$INIT_BIN" --root "$REPO"
[[ "$RC" == 0 ]] && ok "existing manifest, no --force: still exits 0" || nope "existing manifest, no --force: still exits 0 (got $RC)"
chk "existing manifest, no --force: says it left it alone" "already exists; leaving it alone"
MANIFEST=$(cat "$REPO/.tandem.json")
[[ "$(jq -r '.plans_dir' <<<"$MANIFEST")" == "custom" ]] && ok "existing manifest content is untouched" || nope "existing manifest content is untouched"

# --- --force overwrites it ------------------------------------------------------
run "$INIT_BIN" --root "$REPO" --force
[[ "$RC" == 0 ]] && ok "--force: exits 0" || nope "--force: exits 0 (got $RC)"
MANIFEST=$(cat "$REPO/.tandem.json")
[[ "$(jq -r '.plans_dir' <<<"$MANIFEST")" == "docs/plans" ]] && ok "--force overwrote the manifest with detected settings" || nope "--force overwrote the manifest with detected settings"

# --- --force over a manifest that is not a JSON object ---
printf '[1]\n' > "$REPO/.tandem.json"
run env INVALID_MANIFEST="$REPO/.tandem.json" "$INIT_BIN" --root "$REPO" --force
[[ "$RC" == 0 ]] && ok "--force over an invalid manifest exits 0" || nope "--force over an invalid manifest exits 0 (got $RC)"
WARN="tandem-init: $REPO/.tandem.json is not a JSON object, so its settings were ignored."
[[ "$(grep -cF "$WARN" <<<"$OUT")" == 1 ]] && ok "it warns about the invalid manifest once" || nope "it warns about the invalid manifest once: $OUT"
[[ "$(jq -r '.plans_dir' "$REPO/.tandem.json" 2>/dev/null)" == "docs/plans" ]] && ok "it rewrote the invalid manifest" || nope "it rewrote the invalid manifest"

# --- the manifest cannot be written ---
REPO="$TMP/readonly"; mkdir -p "$REPO"; chmod 555 "$REPO"
run "$INIT_BIN" --root "$REPO"
[[ "$RC" == 1 ]] && ok "an unwritable manifest exits 1" || nope "an unwritable manifest exits 1 (got $RC)"
chk "an unwritable manifest says so" "could not write $REPO/.tandem.json."
chmod 755 "$REPO"

# --- git repo: adds both .gitignore entries, once each ------------------------
REPO="$TMP/gitrepo"; mkdir -p "$REPO"
git -C "$REPO" init -q
run "$INIT_BIN" --root "$REPO"
[[ "$RC" == 0 ]] && ok "git repo: exits 0" || nope "git repo: exits 0 (got $RC)"
[[ -f "$REPO/.gitignore" ]] && ok "git repo: .gitignore was created" || nope "git repo: .gitignore was created"
chk "git repo: says it added the relay entry" "added .tandem-relay/ to .gitignore"
chk "git repo: says it added the log entry" "added .tandem-log/ to .gitignore"
RELAY_COUNT=$(grep -cE '^/?\.tandem-relay/?$' "$REPO/.gitignore" 2>/dev/null || true)
[[ "${RELAY_COUNT:-0}" -eq 1 ]] && ok "the relay entry appears exactly once" || nope "the relay entry appears exactly once (got ${RELAY_COUNT:-0})"
LOG_COUNT=$(grep -cE '^/?\.tandem-log/?$' "$REPO/.gitignore" 2>/dev/null || true)
[[ "${LOG_COUNT:-0}" -eq 1 ]] && ok "the log entry appears exactly once" || nope "the log entry appears exactly once (got ${LOG_COUNT:-0})"

# --- re-running it in the same git repo does not duplicate either entry -------
run "$INIT_BIN" --root "$REPO" --force
chk "second run: recognises the existing relay entry" "already ignores .tandem-relay/"
chk "second run: recognises the existing log entry" "already ignores .tandem-log/"
RELAY_COUNT=$(grep -cE '^/?\.tandem-relay/?$' "$REPO/.gitignore" 2>/dev/null || true)
[[ "${RELAY_COUNT:-0}" -eq 1 ]] && ok "second run: the relay entry is still exactly one line" || nope "second run: the relay entry is still exactly one line (got ${RELAY_COUNT:-0})"
LOG_COUNT=$(grep -cE '^/?\.tandem-log/?$' "$REPO/.gitignore" 2>/dev/null || true)
[[ "${LOG_COUNT:-0}" -eq 1 ]] && ok "second run: the log entry is still exactly one line" || nope "second run: the log entry is still exactly one line (got ${LOG_COUNT:-0})"

# --- detection gaps produce NOTE lines -----------------------------------------
REPO="$TMP/gaps"; mkdir -p "$REPO"
OUT=$(PATH="$TMP/bin:$PATH" PROJECT_GAPS=1 "$INIT_BIN" --root "$REPO" 2>&1); RC=$?
chk "no plans_dir: NOTE fires" "NOTE no plans directory was found. Set plans_dir in .tandem.json, or pass a plan path to tandem-handoff."
chk "no check_command: NOTE fires" "NOTE no check command was found. Set check_command so the builder can check its work."
chk "no conventions_file: NOTE fires" "NOTE no AGENTS.md was found, so the builder starts with no project conventions."

# --- a fully detected repo produces no NOTE lines ------------------------------
REPO="$TMP/nogaps"; mkdir -p "$REPO"
run "$INIT_BIN" --root "$REPO"
if [[ "$OUT" == *NOTE* ]]; then
  nope "a fully detected repo: no NOTE lines"
else
  ok "a fully detected repo: no NOTE lines"
fi

printf '\ntandem-init: passed %d, failed %d\n' "$PASS" "$FAIL"
(( FAIL == 0 ))

#!/usr/bin/env bash
# tandem-init writes .tandem.json from detected settings and adds the relay
# mailbox and Q&A log to .gitignore.
#
# Hermetic: tandem, tandem-project and jq (symlinked) are stubbed.
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
ln -s "$(command -v jq)" "$TMP/bin/jq"

# tandem-project's detected settings; PROJECT_JSON overrides them.
cat > "$TMP/bin/tandem-project" <<'STUB'
#!/usr/bin/env bash
if [[ -n "${PROJECT_JSON:-}" ]]; then
  printf '%s\n' "$PROJECT_JSON"
else
  printf '{"plans_dir":"docs/plans","check_command":"true","conventions_file":"AGENTS.md","builder_kind":"codex","prompt_extra":"","source":"detected"}\n'
fi
STUB
# `tandem paths` resolves relative to the given --root, like the real one.
cat > "$TMP/bin/tandem" <<'STUB'
#!/usr/bin/env bash
[[ "${TANDEM_RC:-0}" == 0 ]] || exit "${TANDEM_RC}"
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
TRIMMED="$TMP/trimmed-bin"; mkdir -p "$TRIMMED"
ln -s "$TMP/bin/jq" "$TRIMMED/jq"
ln -s "$(command -v bash)" "$TRIMMED/bash"
# tandem-project deliberately absent from this PATH.
OUT=$(PATH="$TRIMMED" "$INIT_BIN" --root "$TMP" 2>&1); RC=$?
[[ "$RC" == 1 ]] && ok "missing tandem-project on PATH exits 1" || nope "missing tandem-project on PATH exits 1 (got $RC)"
chk "missing tandem-project on PATH names it" "tandem-project not found on PATH"

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
chk "non-git repo skips .gitignore (relay)" "not a git repo — skipped .gitignore (the relay will use"
chk "non-git repo skips .gitignore (log)" "not a git repo — skipped .gitignore (the log will use"
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
OUT=$(PATH="$TMP/bin:$PATH" \
        PROJECT_JSON='{"plans_dir":"","check_command":"","conventions_file":"","builder_kind":"codex","prompt_extra":"","source":"detected"}' \
        "$INIT_BIN" --root "$REPO" 2>&1); RC=$?
chk "no plans_dir: NOTE fires" "NOTE no plans directory found"
chk "no check_command: NOTE fires" "NOTE no check command detected"
chk "no conventions_file: NOTE fires" "NOTE no AGENTS.md"

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

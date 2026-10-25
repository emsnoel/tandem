#!/usr/bin/env bash
# Planner- and repo-supplied text is data and must never execute.
#
# prompt_extra comes from the repo and --because from an agent; both go into
# the launch prompt. A canary file appears if anything is evaluated. Each
# input has its own payload and canary, because they take different paths and
# a shared payload would let one hide a broken other.
set -uo pipefail
HANDOFF="${1:-$(cd "$(dirname "$0")/.." && pwd)/bin/tandem-handoff}"
[ -x "$HANDOFF" ] || { echo "not executable: $HANDOFF" >&2; exit 2; }

PASS=0; FAIL=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }
chk()  { case "$OUT" in *"$2"*) ok "$1" ;; *) nope "$1 (missing: $2)" ;; esac; }

TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
mkdir -p "$TMP/bin" "$TMP/repo/docs/plans" "$TMP/repo/.tandem-relay/reviews"
PLAN="$TMP/repo/docs/plans/p.md"; printf '# plan\n' > "$PLAN"
CANARY_EXTRA="$TMP/canary-extra"
CANARY_BECAUSE="$TMP/canary-because"
printf 'objection text\n' > "$TMP/repo/.tandem-relay/reviews/20260910-000001-1-1.md"
printf '{"plan":"%s","verdict":"objections","kind":"codex","autonomy":"auto","override_reason":""}\n' \
  "$PLAN" > "$TMP/repo/.tandem-relay/reviews/20260910-000001-1-1.json"

# Each payload has a backtick form and a $(...) form.
PAYLOAD_EXTRA='before `touch '"$CANARY_EXTRA"'` and $(touch '"$CANARY_EXTRA"') after'
PAYLOAD_BECAUSE='before `touch '"$CANARY_BECAUSE"'` and $(touch '"$CANARY_BECAUSE"') after'

cat > "$TMP/bin/tandem" <<'STUB'
#!/usr/bin/env bash
[ "$1 $2" = "build start" ] && { printf '{"task":1,"total":1,"title":"the plan","brief":"/relay/build/task-1.md","ledger":"","resumed":false}\n'; exit 0; }
case "$1 $2" in
  "agent snapshot") printf '%%1\t%s\tidle\n' "$REPO"; exit 0 ;;
  "gate review")    printf '20260910-000001-1-1\n'; exit 0 ;;
esac
case "$1" in
  paths)  printf '%s/.tandem-relay\n' "$REPO"; exit 0 ;;
esac
exit 0
STUB
cat > "$TMP/bin/tandem-project" <<STUB
#!/usr/bin/env bash
jq -n --arg root "$TMP/repo" --arg extra '$PAYLOAD_EXTRA' \\
  '{root:\$root, plans_dir:"docs/plans", check_command:"true",
    conventions_file:"", prompt_extra:\$extra, source:"stub"}'
STUB
# Symlink, not copy: macOS kills a copied /usr/bin/jq on exec.
ln -s "$(command -v jq)" "$TMP/bin/jq"
printf '#!/usr/bin/env bash\nexit 0\n' > "$TMP/bin/herdr"
printf '#!/usr/bin/env bash\nexit 0\n' > "$TMP/bin/tandem-review"
chmod +x "$TMP/bin"/*

OUT=$(PATH="$TMP/bin:$PATH" HERDR_ENV=1 REPO="$TMP/repo" \
        "$HANDOFF" "$PLAN" --dry-run \
        --override-review 20260910-000001-1-1 \
        --because "$PAYLOAD_BECAUSE" 2>&1)

if [ -e "$CANARY_EXTRA" ]; then
  nope "CODE EXECUTED via prompt_extra (dry-run) -- the canary file was created"
else
  ok "prompt_extra did not execute (dry-run)"
fi
if [ -e "$CANARY_BECAUSE" ]; then
  nope "CODE EXECUTED via the ruling (dry-run) -- the canary file was created"
else
  ok "the ruling did not execute (dry-run)"
fi

# The dry-run pre-check prints the reason too, so assert against the rendered
# prompt block only.
_prompt_only=$(printf '%s\n' "$OUT" | sed -n '/--- prompt that would be sent ---/,/--- end ---/p')
OUT="$_prompt_only"

# Each input is checked against its own payload and canary.
chk "prompt_extra's ruling appears literally"      'touch '"$CANARY_EXTRA"
chk "prompt_extra's backtick survives verbatim"    '`touch '"$CANARY_EXTRA"
chk "prompt_extra's command substitution is inert" '$(touch '"$CANARY_EXTRA"
chk "the because ruling appears literally"         'touch '"$CANARY_BECAUSE"
chk "the because backtick survives verbatim"       '`touch '"$CANARY_BECAUSE"
chk "the because command substitution is inert"    '$(touch '"$CANARY_BECAUSE"

# --dry-run skips the sidecar write, so repeat without it to exercise the jq
# write and read-back.
OUT2=$(PATH="$TMP/bin:$PATH" HERDR_ENV=1 REPO="$TMP/repo" \
         "$HANDOFF" "$PLAN" \
         --override-review 20260910-000001-1-1 \
         --because "$PAYLOAD_BECAUSE" 2>&1)

if [ -e "$CANARY_EXTRA" ]; then
  nope "CODE EXECUTED via prompt_extra (real write) -- the canary file was created"
else
  ok "prompt_extra did not execute (real write)"
fi
if [ -e "$CANARY_BECAUSE" ]; then
  nope "CODE EXECUTED via the ruling (real write) -- the canary file was created"
else
  ok "the ruling did not execute (real write)"
fi

STORED=$(jq -r '.override_reason' \
  "$TMP/repo/.tandem-relay/reviews/20260910-000001-1-1.json" 2>/dev/null)
if [ "$STORED" = "$PAYLOAD_BECAUSE" ]; then
  ok "the ruling survives the sidecar round-trip byte for byte"
else
  nope "the sidecar mangled the ruling (got: $STORED)"
fi

# The next plain handoff adopts the stored ruling from the sidecar, a second
# way into the same template, so it gets the same canary.
OUT3=$(PATH="$TMP/bin:$PATH" HERDR_ENV=1 REPO="$TMP/repo" \
         "$HANDOFF" "$PLAN" --dry-run 2>&1)

if [ -e "$CANARY_BECAUSE" ]; then
  nope "CODE EXECUTED via the adopted ruling -- the canary file was created"
else
  ok "the adopted ruling did not execute"
fi
case "$OUT3" in
  *"OVERRULED OBJECTIONS"*) ok "the adopted ruling renders its block" ;;
  *) nope "the adopted ruling did not render (the re-handoff path is dead)" ;;
esac
case "$OUT3" in
  *'touch '"$CANARY_BECAUSE"*) ok "the adopted ruling reaches the builder literally" ;;
  *) nope "the adopted ruling was mangled on the way to the prompt" ;;
esac
# A stored reason with a live slot marker must reach the builder defanged.
printf '{"plan":"%s","verdict":"objections","kind":"codex","autonomy":"auto","tree":"","override_reason":"use {{EXTRA}} and {{{OVERRULED}}} with care"}\n' \
  "$PLAN" > "$TMP/repo/.tandem-relay/reviews/20260910-000001-1-1.json"
OUT4=$(PATH="$TMP/bin:$PATH" HERDR_ENV=1 REPO="$TMP/repo" \
         "$HANDOFF" "$PLAN" --dry-run 2>&1)
chk_out4() { case "$OUT4" in *"$2"*) ok "$1" ;; *) nope "$1 (missing: $2)" ;; esac; }
chk_out4 "an adopted reason's slot markers are defanged" "use {EXTRA} and {OVERRULED} with care"
_n=$(printf '%s' "$OUT4" | grep -o 'with care' | wc -l | tr -d ' ')
case "$_n" in
  1) ok "the adopted ruling is not duplicated by its own slot marker" ;;
  *) nope "the adopted ruling appears $_n times, want 1" ;;
esac

printf '\nprompt-injection: passed %d, failed %d\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

# shellcheck shell=bash
# stub_handoff_env DIR: stubs that let bin/tandem-handoff and bin/tandem-review
# render and "send" a prompt with no Herdr server, panes or network. Shared by
# the handoff suites and the external token eval.
#
# Read at run time:
#   REPO               fixture repo root (required)
#   STUB_CHECK         check_command; unset is "true", set but empty is none
#   STUB_PROMPT_EXTRA  prompt_extra
# DIR/bin/tandem.extra, if present, is sourced first by the tandem stub, so its
# cases win. By default `build start` answers task 1 of 1.
stub_handoff_env() {
  local dir="$1"
  mkdir -p "$dir/bin"
  printf '#!/usr/bin/env bash\nexit 0\n' > "$dir/bin/herdr"
  printf '#!/usr/bin/env bash\nexit 0\n' > "$dir/bin/tandem-review"
  cat > "$dir/bin/tandem" <<'STUB'
#!/usr/bin/env bash
_extra="$(dirname "$0")/tandem.extra"
[ -f "$_extra" ] && . "$_extra"
if [ "$1 $2" = "build start" ]; then
  case " $* " in *" --judge "*) j=true ;; *) j=false ;; esac
  printf '{"task":1,"total":1,"title":"the plan","brief":"/relay/build/task-1.md","ledger":"","resumed":false,"judge":%s}\n' "${STUB_BUILD_JUDGE:-$j}"
  exit 0
fi
case "$1 $2" in
  "agent snapshot") printf '%%1\t%s\tidle\n' "$REPO"; exit 0 ;;
  "gate review")    exit 0 ;;
esac
case "$1" in
  paths) printf '%s/.tandem-relay\n' "$REPO"; exit 0 ;;
esac
exit 0
STUB
  cat > "$dir/bin/tandem-project" <<'STUB'
#!/usr/bin/env bash
jq -n --arg root "$REPO" --arg check "${STUB_CHECK-true}" --arg extra "${STUB_PROMPT_EXTRA-}" \
  '{root:$root, plans_dir:"docs/plans", check_command:$check,
    conventions_file:"", prompt_extra:$extra, source:"stub"}'
STUB
  chmod +x "$dir/bin/herdr" "$dir/bin/tandem-review" "$dir/bin/tandem" "$dir/bin/tandem-project"
  # Symlink after the chmod, so chmod never touches the real jq.
  ln -s "$(command -v jq)" "$dir/bin/jq"
}

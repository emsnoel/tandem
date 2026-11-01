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
# cases win. By default `build start` answers task 1 of 1. A jq that fails on
# any call sits on PATH.
stub_handoff_env() {
  local dir="$1"
  mkdir -p "$dir/bin"
  printf '#!/usr/bin/env bash\nexit 0\n' > "$dir/bin/herdr"
  printf '#!/usr/bin/env bash\nexit 0\n' > "$dir/bin/tandem-review"
  cat > "$dir/bin/tandem" <<'STUB'
#!/usr/bin/env bash
nul() { local f; for f in "$@"; do printf '%s\0' "$f"; done; }
_extra="$(dirname "$0")/tandem.extra"
[ -f "$_extra" ] && . "$_extra"
if [ "$1 $2" = "build start" ]; then
  case " $* " in *" --judge "*) j=true ;; *) j="" ;; esac
  j="${STUB_BUILD_JUDGE:-$j}"; [ "$j" = false ] && j=""
  # engine run_id dry_run task total title brief ledger resumed replaced judge
  # reviewer review_pending review_prompt
  nul "" "" "" 1 1 "the plan" /relay/build/task-1.md "" "" "" "$j" "" "" ""
  exit 0
fi
case "$1 $2" in
  # root plans_dir check_command conventions_file builder_kind prompt_extra
  # task_input_budget context_limit build_engine review_delivery
  # acceptance_policy collaboration source invalid_manifest
  # The JSON is what bin/tandem-project prints, for tandem-review.
  "config project")
    case " $* " in
      *" --fields "*) nul "$REPO" docs/plans "${STUB_CHECK-true}" "" "" "${STUB_PROMPT_EXTRA-}" "" "" "" "" "" "" stub "" ;;
      *) printf '{"root":"%s","plans_dir":"docs/plans","check_command":"%s","conventions_file":"","prompt_extra":"%s","source":"stub"}\n' \
           "$REPO" "${STUB_CHECK-true}" "${STUB_PROMPT_EXTRA-}" ;;
    esac
    exit 0 ;;
  "agent snapshot") printf '%%1\t%s\tidle\n' "$REPO"; exit 0 ;;
  "gate review")    exit 0 ;;
esac
case "$1" in
  paths) printf '%s/.tandem-relay\n' "$REPO"; exit 0 ;;
esac
exit 0
STUB
  chmod +x "$dir/bin/herdr" "$dir/bin/tandem-review" "$dir/bin/tandem"
  # A jq that fails: tandem-handoff asks tandem, so any jq call is a bug.
  printf '#!/bin/sh\necho "jq was called: $*" >&2\nexit 99\n' > "$dir/bin/jq"
  chmod +x "$dir/bin/jq"
}

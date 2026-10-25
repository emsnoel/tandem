#!/usr/bin/env bash
# Probes the installed agent CLIs for the flags the adapters emit.
#
# Not in tests/run.sh: that suite must pass with none of these CLIs installed.
# It proves a flag exists, not that its meaning is unchanged.
set -uo pipefail

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }
skip() { printf '  skip %s (not installed)\n' "$1"; SKIP=$((SKIP+1)); }

probe() { # probe <cli> <help-args> <flag>...
  local cli="$1"; shift
  local helpargs="$1"; shift
  if ! command -v "$cli" >/dev/null 2>&1; then skip "$cli"; return; fi
  local help; help=$($cli $helpargs 2>&1)
  local f
  for f in "$@"; do
    case "$help" in
      *"$f"*) ok "$cli $f" ;;
      *)      nope "$cli $f no longer in --help" ;;
    esac
  done
}

probe codex    --help -s --sandbox -a --ask-for-approval -m -C --dangerously-bypass-approvals-and-sandbox --dangerously-bypass-hook-trust
probe claude   --help --permission-mode --model --effort
probe opencode --help -m --model --agent --auto
probe copilot  --help --model --effort --mode --allow-all-tools --allow-all-urls --allow-all --no-ask-user -C

# opencode's --variant is `run`-only. If it appears here, opencode has gained
# an effort setting and Supports("effort") should change.
if command -v opencode >/dev/null 2>&1; then
  case "$(opencode --help 2>&1)" in
    *--variant*) nope "opencode --variant now exists on the interactive path; revisit Supports(effort)" ;;
    *)           ok   "opencode --variant still absent from the interactive path" ;;
  esac
fi

printf '\ncli-drift: passed %d, failed %d, skipped %d\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]

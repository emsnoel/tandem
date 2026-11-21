# Sourced by tests/run.sh, and by tests/test-suite-env.sh to test it.
#
# run_suites JOBS SUITE...: run each suite with bash, at most JOBS at once.
# Each suite's output prints whole, in the order given, as soon as it and every
# suite before it have finished, so a parallel run reads like a serial one.
# Adds one to FAILED for each suite that exits non-zero, and names a suite
# that exits non-zero without printing a FAIL line. Needs hdr() and FAILED.
#
# macOS ships bash 3.2, which has no `wait -n`. Waiting on the oldest running
# suite first keeps the output in order and costs little: a later suite that
# finishes early just waits to be printed.
run_suites() {
  local jobs="$1"; shift
  [ "$#" -eq 0 ] && return 0
  local dir; dir=$(mktemp -d)
  local pids=() names=() n=0 printed=0 t
  for t in "$@"; do
    names[n]=$(basename "$t" .sh)
    ( bash "$t" > "$dir/$n.log" 2>&1; echo "$?" > "$dir/$n.rc" ) &
    pids[n]=$!
    n=$((n+1))
    if [ $((n - printed)) -ge "$jobs" ]; then
      _run_suites_print "$dir" "$printed" "${names[printed]}" "${pids[printed]}"
      printed=$((printed+1))
    fi
  done
  while [ "$printed" -lt "$n" ]; do
    _run_suites_print "$dir" "$printed" "${names[printed]}" "${pids[printed]}"
    printed=$((printed+1))
  done
  rm -rf "$dir"
}

# _run_suites_print DIR INDEX NAME PID: wait for one suite, then print it.
_run_suites_print() {
  local rc
  wait "$4"
  rc=$(cat "$1/$2.rc" 2>/dev/null || echo 1)
  hdr "$3"
  cat "$1/$2.log"
  if [ "$rc" -ne 0 ]; then
    printf '  FAIL %s (exit %d)\n' "$3" "$rc"
    FAILED=$((FAILED+1))
  fi
}

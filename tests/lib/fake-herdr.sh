# shellcheck shell=bash
# fake_set_state STATUS [SEQ]: set the agent's status and lifecycle counter
# together. FAKE/state holds "STATUS SEQ" and is replaced whole, so the fake
# event socket never reads a mismatched pair. SEQ defaults to the current one,
# plus one when the status changes. Needs FAKE in the environment.
fake_set_state() {
  local cur_st cur_seq seq
  read -r cur_st cur_seq < "$FAKE/state"
  seq="${2:-}"
  if [ -z "$seq" ]; then
    seq=$cur_seq; [ "$1" = "$cur_st" ] || seq=$((cur_seq+1))
  fi
  printf '%s %s\n' "$1" "$seq" > "$FAKE/state.$$" && mv -f "$FAKE/state.$$" "$FAKE/state"
}
# fake_pin NAME STATUS SEQ: give pane NAME its own status, which the shared
# state no longer moves. fake_unpin NAME undoes it.
fake_pin() { printf '%s %s\n' "$2" "$3" > "$FAKE/state.$1.$$" && mv -f "$FAKE/state.$1.$$" "$FAKE/state.$1"; }
fake_unpin() { rm -f "$FAKE/state.$1"; }
# fake_status: print the agent's current status.
fake_status() { local s _; read -r s _ < "$FAKE/state"; printf '%s\n' "$s"; }

# fake_herdr_install BIN FAKE: a `herdr` script in BIN that keeps one agent's
# status, session and lifecycle counter in files under FAKE.
#
# A typed prompt moves the agent to "working" and is logged to FAKE/prompts
# (pane and prompt) and FAKE/prompt.N.txt. A reset command (/new, /clear)
# starts a new session and is logged to FAKE/resets. A "blocked" agent never
# moves. Call fake_set_state idle to end a turn. `agent list` shows builder,
# planner and reviewer in panes w1:<name>. Names in FAKE/absent have no pane
# and get agent_not_found (exit 10).
#
# FAKE/end_after N: the Nth read after a prompt finds the agent idle again, as
# for a rejected model. `agent read` prints FAKE/pane.
#
# FAKE/kind is the agent kind (codex by default). For copilot, /new moves
# neither Herdr's session nor its counter, and the process log under
# $COPILOT_HOME/logs records the new conversation. A codex agent writes each
# prompt to today's rollout under $CODEX_HOME/sessions when CODEX_HOME is set.
#
# Read at run time: FAKE, REPO (the agent's cwd), COPILOT_HOME, CODEX_HOME.
fake_herdr_install() {
  local bin="$1" fake="$2"
  mkdir -p "$bin" "$fake"
  printf 'idle 10\n' > "$fake/state"; printf 'sess-1\n' > "$fake/session"
  cat > "$bin/herdr" <<'STUB'
#!/usr/bin/env bash
# put FILE VALUE: replace a state file whole, so the fake socket never reads a
# partial write.
put() { printf '%s\n' "$2" > "$1.$$" && mv -f "$1.$$" "$1"; }
put_state() { put "$FAKE/state" "$1 $2"; }
# bump_gets: count a read toward end_after, under an mkdir lock.
lock() { until mkdir "$FAKE/gets.lock" 2>/dev/null; do sleep 0.01; done; }
unlock() { rmdir "$FAKE/gets.lock"; }
# count_read: during a counted turn, count this read and end the turn on the
# Nth. The state is read after the count, so a prompt that just landed is not
# miscounted.
count_read() {
  [ -s "$FAKE/end_after" ] && [ -f "$FAKE/gets" ] || return 0
  lock
  n=$(( $(cat "$FAKE/gets") + 1 )); put "$FAKE/gets" "$n"
  read -r st seq < "$FAKE/state"
  if [ "$n" -eq "$(cat "$FAKE/end_after")" ]; then
    st=idle; seq=$((seq+1)); put_state idle "$seq"
  fi
  unlock
}
read -r st seq < "$FAKE/state"; sess=$(cat "$FAKE/session")
kind=$(cat "$FAKE/kind" 2>/dev/null); kind=${kind:-codex}
if [ "$1" = agent ] && [ -n "${3:-}" ] && [ -f "$FAKE/absent" ] && grep -qxF -- "$3" "$FAKE/absent"; then
  printf '{"error":{"code":"agent_not_found","message":"agent target %s not found"}}\n' "$3" >&2
  exit 1
fi
case "$1 $2" in
  "status server")
    printf 'status: running\nsocket: %s\n' "${HERDR_SOCKET_PATH:-}"
    exit 0 ;;
  "agent list")
    count_read
    # builder, planner and reviewer, each with the shared or pinned state and
    # session, minus FAKE/absent.
    sep=""; printf '{"id":"x","result":{"agents":['
    for n in builder planner reviewer; do
      [ -f "$FAKE/absent" ] && grep -qxF -- "$n" "$FAKE/absent" && continue
      nst=$st; nseq=$seq; nsess=$sess
      [ -f "$FAKE/state.$n" ] && read -r nst nseq < "$FAKE/state.$n"
      [ -f "$FAKE/session.$n" ] && nsess=$(cat "$FAKE/session.$n")
      printf '%s{"agent":"%s","agent_status":"%s","cwd":"%s","name":"%s","pane_id":"w1:%s","state_change_seq":%s,"agent_session":{"value":"%s"}}' \
        "$sep" "$kind" "$nst" "$REPO" "$n" "$n" "$nseq" "$nsess"
      sep=","
    done
    printf '],"type":"agent_list"}}\n'
    exit 0 ;;
  "agent get")
    count_read
    [ -f "$FAKE/state.$3" ] && read -r st seq < "$FAKE/state.$3"
    [ -f "$FAKE/session.$3" ] && sess=$(cat "$FAKE/session.$3")
    printf '{"id":"x","result":{"agent":{"agent":"%s","agent_status":"%s","cwd":"%s","name":"%s","pane_id":"w1:%s","state_change_seq":%s,"agent_session":{"value":"%s"}},"type":"agent_info"}}\n' "$kind" "$st" "$REPO" "$3" "$3" "$seq" "$sess"
    exit 0 ;;
  "agent prompt")
    if [ "$4" = "/new" ] && [ "$kind" = copilot ]; then
      # Copilot: Herdr sees nothing; the process log names the new conversation.
      n=$(( $(wc -l < "$FAKE/resets" 2>/dev/null || echo 0) + 1 ))
      mkdir -p "$COPILOT_HOME/logs"
      printf '%s [INFO] Registering foreground session: copilot-%s\n' \
        "$(perl -MTime::HiRes=time -MPOSIX=strftime -e '$t=time; printf "%s.%03dZ", strftime("%Y-%m-%dT%H:%M:%S", gmtime($t)), ($t-int($t))*1000')" "$n" \
        >> "$COPILOT_HOME/logs/process-1-4242.log"
      printf '%s\t%s\n' "$3" "$4" >> "$FAKE/resets"
      exit 0
    fi
    if [ "$4" = "/new" ] || [ "$4" = "/clear" ]; then
      # A reset starts a new conversation with a brief turn: the counter moves while
      # the status does not.
      put "$FAKE/session" "sess-$(( ${sess#sess-} + 1 ))"; put_state "$st" "$((seq+2))"
      printf '%s\t%s\n' "$3" "$4" >> "$FAKE/resets"
      exit 0
    fi
    if [ "$kind" = codex ] && [ -n "${CODEX_HOME:-}" ]; then
      day="$CODEX_HOME/sessions/$(date +%Y/%m/%d)"; mkdir -p "$day"
      log="$day/rollout-fake-$sess.jsonl"
      [ -f "$log" ] || printf '{"type":"session_meta","payload":{"id":"%s"}}\n' "$sess" > "$log"
      jq -cn --arg text "$4" '{type:"response_item",payload:{type:"message",role:"user",content:[{type:"input_text",text:$text}]}}' >> "$log"
    fi
    printf '%s\t%s\n' "$3" "$(printf '%s' "$4" | tr '\n' ' ')" >> "$FAKE/prompts"
    printf '%s' "$4" > "$FAKE/prompt.$(wc -l < "$FAKE/prompts" | tr -d ' ').txt"
    [ "$st" = blocked ] || { put_state working "$((seq+1))"; }
    lock; put "$FAKE/gets" 0; unlock
    exit 0 ;;
  "agent read")
    cat "$FAKE/pane" 2>/dev/null
    exit 0 ;;
  "pane process-info")
    printf '{"id":"x","result":{"process_info":{"pane_id":"%s","shell_pid":1,"foreground_process_group_id":4242,"foreground_processes":[{"name":"%s","argv0":"%s","pid":4242}]},"type":"pane_process_info"}}\n' "$4" "$kind" "$kind"
    exit 0 ;;
esac
exit 0
STUB
  chmod +x "$bin/herdr"
}

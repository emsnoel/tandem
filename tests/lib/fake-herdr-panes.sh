# shellcheck shell=bash
# fake_herdr_panes_install BIN: put a herdr in BIN that holds several panes and
# named agents, in front of the single-agent fake that fake_herdr_install left
# at BIN/herdr.fake. Needs FAKE in the environment.
#
# The single-agent fake keeps the status and logs prompts. This wrapper keeps
# the panes: FAKE/agents has one line per pane, "pane<TAB>name<TAB>cwd<TAB>kind<TAB>tab",
# with "-" for no name.
fake_herdr_panes_install() {
  cat > "$1/herdr" <<'STUB'
#!/usr/bin/env bash
A="$FAKE/agents"
printf '%s\n' "$*" >> "$FAKE/calls"
# row FIELD VALUE: the first line whose FIELD (1 pane, 2 name) is VALUE.
row() { awk -F'\t' -v f="$1" -v v="$2" '$f == v { print; exit }' "$A"; }
# set_row PANE LINE: replace PANE's line, or drop it when LINE is empty.
set_row() {
  awk -F'\t' -v p="$1" -v l="$2" '$1 == p { if (l != "") print l; next } { print }' "$A" > "$A.$$" && mv -f "$A.$$" "$A"
}
# agent_json LINE: one agent object with the shared status.
agent_json() {
  local pane name cwd kind tab st seq sess n
  IFS=$'\t' read -r pane name cwd kind tab <<<"$1"
  read -r st seq < "$FAKE/state"; sess=$(cat "$FAKE/session")
  n="\"$name\""; [ "$name" = - ] && n=null
  printf '{"agent":"%s","agent_status":"%s","cwd":"%s","name":%s,"pane_id":"%s","tab_id":"%s","state_change_seq":%s,"agent_session":{"value":"%s"}}' \
    "$kind" "$st" "$cwd" "$n" "$pane" "$tab" "$seq" "$sess"
}
not_found() { printf '{"error":{"code":"agent_not_found","message":"agent target %s not found"}}\n' "$1" >&2; exit 1; }
case "$1 $2" in
  "agent list")
    sep=""; printf '{"id":"x","result":{"agents":['
    while IFS= read -r l; do printf '%s' "$sep"; agent_json "$l"; sep=","; done < "$A"
    printf '],"type":"agent_list"}}\n'; exit 0 ;;
  "agent get")
    l=$(row 2 "$3"); [ -n "$l" ] || l=$(row 1 "$3"); [ -n "$l" ] || not_found "$3"
    printf '{"id":"x","result":{"agent":%s,"type":"agent_info"}}\n' "$(agent_json "$l")"; exit 0 ;;
  "agent prompt"|"agent read"|"agent wait"|"agent send-keys")
    l=$(row 2 "$3"); [ -n "$l" ] || l=$(row 1 "$3"); [ -n "$l" ] || not_found "$3"
    exec "$(dirname "$0")/herdr.fake" "$@" ;;
  "agent rename")
    IFS=$'\t' read -r pane _ cwd kind tab <<<"$(row 1 "$3")"
    set_row "$3" "$(printf '%s\t%s\t%s\t%s\t%s' "$pane" "$4" "$cwd" "$kind" "$tab")"
    printf '{"result":{}}\n'; exit 0 ;;
  "agent start")
    name="$3"; shift 3; pane=""; kind=""
    while [ $# -gt 0 ]; do
      case "$1" in --pane) pane="$2"; shift 2 ;; --kind) kind="$2"; shift 2 ;; --) break ;; *) shift ;; esac
    done
    IFS=$'\t' read -r _ _ cwd _ tab <<<"$(row 1 "$pane")"
    set_row "$pane" "$(printf '%s\t%s\t%s\t%s\t%s' "$pane" "$name" "$cwd" "$kind" "$tab")"
    printf '{"result":{"agent":{"name":"%s"}}}\n' "$name"; exit 0 ;;
  "pane split")
    parent="$3"; shift 3; cwd=""
    while [ $# -gt 0 ]; do case "$1" in --cwd) cwd="$2"; shift 2 ;; *) shift ;; esac; done
    IFS=$'\t' read -r _ _ _ _ tab <<<"$(row 1 "$parent")"
    n=$(( $(cat "$FAKE/next" 2>/dev/null || echo 10) + 1 )); printf '%s\n' "$n" > "$FAKE/next"
    pane="${parent%%:*}:p$n"
    # A shell until an agent starts in it.
    printf '%s\t-\t%s\tshell\t%s\n' "$pane" "$cwd" "$tab" >> "$A"
    printf '{"result":{"pane":{"pane_id":"%s"}}}\n' "$pane"; exit 0 ;;
  "pane close")
    set_row "$3" ""; printf '{"result":{}}\n'; exit 0 ;;
  "pane run"|"pane rename"|"pane title"|"pane report-metadata"|"tab rename"|"tab set-title")
    printf '{"result":{}}\n'; exit 0 ;;
esac
exec "$(dirname "$0")/herdr.fake" "$@"
STUB
  chmod +x "$1/herdr"
}

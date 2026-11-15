# bin/lib/pair-identity.sh: the names of a pair's agents, and its record.
#
# Sourced, never run. Herdr agent names are global, so two worktrees of one
# repository need different names. `tandem paths names` resolves them per role:
# HERDR_PLANNER_NAME, HERDR_BUILDER_NAME or HERDR_REVIEWER_NAME, then the names
# tandem-pair recorded in the checkout's relay (pair.json), then names derived
# from the checkout. The builder's and the reviewer's panes never have the
# planner's environment, so they read the record. The caller defines die.
# The callers set PLANNER_PANE, which shellcheck cannot see from this file.
# shellcheck shell=bash disable=SC2153

# pair_names DIR [--bind|--allow-pending]: set PLANNER_NAME, BUILDER_NAME and
# REVIEWER_NAME for the checkout holding DIR. Without a flag it only reads, and
# fails in a checkout whose names need an id it has not been given. Only
# tandem-pair's real run passes --bind, which gives the checkout that id in the
# state registry. A builder's or reviewer's sandbox cannot write there.
# --allow-pending puts a placeholder in place of the missing id instead.
pair_names() {
  local out rc=0
  # tandem's own error, such as a pair.json that is not JSON, goes with the refusal.
  out=$(tandem paths names --root "$1" ${2:+"$2"} 2>&1) || rc=$?
  (( rc == 0 )) || die "could not resolve the pair's names for $1: $out"
  IFS=$'\t' read -r PLANNER_NAME BUILDER_NAME REVIEWER_NAME <<<"$out" || true
  [[ -n "$PLANNER_NAME" && -n "$BUILDER_NAME" && -n "$REVIEWER_NAME" ]] \
    || die "could not resolve the pair's names for $1"
}

# record_pair MAILBOX: write the names to MAILBOX/pair.json, through a rename so
# a reader never sees half a file. The names match Herdr's pattern, so they
# need no JSON escaping.
record_pair() {
  printf '{"planner":"%s","builder":"%s","reviewer":"%s"}\n' "$PLANNER_NAME" "$BUILDER_NAME" "$REVIEWER_NAME" \
    > "$1/pair.json.tmp" && mv "$1/pair.json.tmp" "$1/pair.json"
}

# check_relay_owner MAILBOX: refuse when MAILBOX belongs to another planner's
# live pair. The relay holds one pair's records and one bridge, so a second
# planner would take over the first pair's verdicts and handbacks. A pair is
# live while its bridge runs. Its owner is the planner that bridge delivers to:
# its --planner argument, then the planner in pair.json (or the checkout's
# derived name). The owner's own pane may pair again under a new name. Reads
# PLANNER_NAME and PLANNER_PANE; needs bridge_alive and bridge_planner
# (lib/bridge-lock.sh).
check_relay_owner() {
  local mb="$1" owner="" pid snap rc=0 pane=""
  pid=$(cat "$mb/bridge.pid" 2>/dev/null || true)
  bridge_alive "$pid" || return 0
  if ! owner=$(bridge_planner "$pid"); then
    # This pane's own HERDR_PLANNER_NAME says nothing about the bridge's owner.
    owner=$(env -u HERDR_PLANNER_NAME tandem paths names --root "$(dirname "$mb")" 2>/dev/null | cut -f1) || owner=""
    [[ -n "$owner" ]] || die "could not tell which planner this checkout's relay bridge delivers to; see $mb/pair.json"
  fi
  [[ "$owner" != "$PLANNER_NAME" ]] || return 0
  snap=$(tandem agent snapshot "$owner" 2>/dev/null) || rc=$?
  if (( rc == 0 )); then
    IFS=$'\t' read -r pane _ _ <<<"$snap"
    [[ "$pane" != "$PLANNER_PANE" ]] || return 0
    die "this checkout already has a live pair, run by planner '$owner' in pane $pane. One checkout holds one pair: close that pair with tandem-unpair from pane $pane, or pair this planner in a separate git worktree."
  fi
  die "this checkout's relay bridge still delivers to planner '$owner', which is gone. Clear the old pair with: HERDR_PLANNER_NAME=$owner tandem-unpair --force"
}

# checkout_of DIR: print the checkout holding DIR, as its git toplevel with
# symlinks resolved, or DIR itself, resolved, when DIR is not inside git. Fails
# for an empty or missing DIR.
checkout_of() {
  local top
  [[ -n "$1" ]] || return 1
  top=$(git -C "$1" rev-parse --show-toplevel 2>/dev/null) || top="$1"
  (cd "$top" 2>/dev/null && pwd -P)
}

# refuse_foreign_pane ROLE NAME PANE CWD: stop the pairing when the agent NAME
# in PANE works in a checkout other than ROOT's. Its messages go to its own
# checkout's relay, so adopting it would cross two pairs. A pane outside git, or
# with no cwd, counts as another checkout. An empty NAME is an unnamed agent
# about to be adopted. Reads ROOT.
refuse_foreign_pane() {
  local here there who="the agent named '$2'"
  here=$(checkout_of "$ROOT") || here="$ROOT"
  there=$(checkout_of "$4") || there=""
  [[ -n "$there" && "$there" == "$here" ]] && return 0
  [[ -n "$2" ]] || who="the unnamed agent"
  printf 'tandem-pair: %s in pane %s works in another checkout: %s.\n' "$who" "$3" "${there:-${4:-an unknown folder}}" >&2
  printf "tandem-pair: tandem does not adopt it, because its messages would go to that checkout's pair.\n" >&2
  printf 'tandem-pair: close it from that checkout with tandem-unpair, or pair here under other names (HERDR_BUILDER_NAME, HERDR_REVIEWER_NAME).\n' >&2
  exit 35
}

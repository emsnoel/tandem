# bin/lib/reviewer.sh: the sentences the scripts print about the reviewer pane.
#
# Sourced, never run. Go makes the decisions (config.LoadReviewer, `tandem
# agent self`); internal/config's NoReviewerRemedy is the Go copy of the remedy.
# Callers resolve their own symlink to find this file.

# shellcheck shell=bash

REVIEWER_REMEDY='Declare a "reviewer" block in .tandem.json and re-run tandem-pair, or run tandem-pair --reviewer KIND.'

# explain_reviewer_self NOUN NOTHING EXPECT ANSWER
#
# Explains the ANSWER of `tandem agent self --expect EXPECT`. NOUN is what only
# the reviewer may do ("a verdict"), NOTHING is what was not done ("NOTHING WAS
# SENT."). Returns 0 for the reviewer pane, warning when only the role said so,
# and otherwise calls the caller's die.
explain_reviewer_self() {
  local noun="$1" nothing="$2" expect="$3" ok why who
  IFS=$'\t' read -r ok why who <<<"$4"
  case "${ok:-}/${why:-}" in
    accept/unreachable)
      printf '%s: warning: Herdr cannot be reached from this shell (pane %s), so the role %s names it. A sandboxed reviewer cannot reach Herdr.\n' \
        "${0##*/}" "${HERDR_PANE_ID:-}" "$who" >&2 ;;
    accept/*) ;;
    refuse/timeout)
      die "Herdr did not say in time which pane this is (pane ${HERDR_PANE_ID:-}), and $noun comes from the reviewer pane only. $nothing Check that Herdr is running, then try again." ;;
    refuse/unnamed|refuse/error)
      die "Herdr could not say which pane this is (pane ${HERDR_PANE_ID:-}${who:+: $who}), and $noun comes from the reviewer pane only. $nothing Check that Herdr is running, then try again." ;;
    refuse/*)
      die "$noun comes from the reviewer pane only ($expect), and this is ${who:-a pane that cannot name itself}. The builder never reviews. $nothing Run this from the reviewer pane." ;;
    *)
      die "tandem could not check which pane this is. $nothing Re-run install.sh, then try again." ;;
  esac
}

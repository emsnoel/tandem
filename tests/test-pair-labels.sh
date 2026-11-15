#!/usr/bin/env bash
# tandem-pair labels each pane "role (kind)" and names the planner's tab
# "tandem: <repo>" while it still has Herdr's default number.
#
# Hermetic: a stub herdr and --dry-run, so no pane is touched.
set -uo pipefail
PAIR="${1:-$(cd "$(dirname "$0")/.." && pwd)/bin/tandem-pair}"
[ -x "$PAIR" ] || { echo "not executable: $PAIR" >&2; exit 2; }

PASS=0; FAIL=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }
SHOWN=0
# On the first miss, show the output; a refusal otherwise reads as a missing
# label.
chk()  { case "$OUT" in *"$2"*) ok "$1" ;; *) nope "$1 (missing: $2)"
           [ "$SHOWN" -eq 1 ] || { SHOWN=1; printf '%s\n' "$OUT" | sed 's/^/       | /'; } ;; esac; }
lack() { case "$OUT" in *"$2"*) nope "$1 (found: $2)" ;; *) ok "$1" ;; esac; }


TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
mkdir -p "$TMP/bin" "$TMP/labelled-repo"
# tandem-pair calls no jq; a call fails the run.
printf '#!/bin/sh\necho "jq was called: $*" >&2\nexit 99\n' > "$TMP/bin/jq"
chmod +x "$TMP/bin/jq"
cat > "$TMP/bin/herdr" <<'STUB'
#!/usr/bin/env bash
case "$1 $2" in
  "agent list")
    printf '{"result":{"agents":[{"pane_id":"w1:p1","agent":"%s","name":"","agent_status":"idle","cwd":"%s","tab_id":"w1:t1"}]}}\n' "${PLANNER_KIND_STUB:-claude}" "$REPO" ;;
  "tab get")
    printf '{"result":{"tab":{"label":"%s","number":84,"tab_id":"w1:t1","workspace_id":"w1"}}}\n' "$TABLABEL" ;;
  "tab list")
    # Herdr labels an unnamed tab with its position, so the planner's tab is third.
    printf '{"result":{"tabs":[{"tab_id":"w1:t7","label":"notes"},{"tab_id":"w1:t8","label":"2"},{"tab_id":"w1:t1","label":"%s"}]}}\n' "$TABLABEL" ;;
  *)
    printf '{"error":{"code":"agent_not_found","message":"agent target %s not found"}}\n' "${3:-}" >&2
    exit 1 ;;
esac
STUB
chmod +x "$TMP/bin/herdr"

REPO=$(cd "$TMP/labelled-repo" && pwd -P); export REPO
git -C "$REPO" init -q

pair() {
  OUT=$(cd "$REPO" && env PATH="$TMP/bin:$PATH" TABLABEL="$1" HERDR_TANDEM_CONFIG="$TMP/none.json" HERDR_ENV=1 HERDR_PANE_ID=w1:p1 HERDR_SKIP_HOOK_CHECK=1 \
    "$PAIR" --dry-run --skip-doctor --builder claude --reviewer claude 2>&1)
}

pair 3
chk "the planner pane is labelled with its kind" 'pane rename w1:p1 planner (claude)'
chk "the builder pane is labelled with its kind" 'builder (claude)'
chk "the reviewer pane is labelled with its kind" 'reviewer (claude)'
chk "a tab that still has its number is named for the repo" 'tab rename w1:t1 tandem: labelled-repo'

pair "my own tab"
chk "a labelled pair still labels its panes" 'builder (claude)'
lack "a tab a person named is left alone" 'tab rename'

pair 84
lack "a tab labelled with its number, not its position, is left alone" 'tab rename'

PLANNER_KIND_STUB=copilot; export PLANNER_KIND_STUB
pair 3
chk "a Copilot planner is labelled with its kind" 'pane rename w1:p1 planner (copilot)'

PLANNER_KIND_STUB=codex
pair 3
chk "a Codex planner is refused" "codex can't plan"
lack "and nothing is renamed" 'pane rename'
unset PLANNER_KIND_STUB

# A linked worktree's panes carry its name after the role, so two sessions on
# one repository read apart in the sidebar. The main checkout keeps "role (kind)".
git -C "$REPO" -c user.name=t -c user.email=t@t commit -q --allow-empty -m init
git -C "$REPO" worktree add -q --detach "$TMP/feat-x"
MAIN="$REPO"; REPO=$(cd "$TMP/feat-x" && pwd -P)
pair 3
chk "a worktree planner pane names its worktree" 'pane rename w1:p1 planner · feat-x (claude)'
chk "a worktree builder pane names its worktree" 'builder · feat-x (claude)'
chk "a worktree reviewer pane names its worktree" 'reviewer · feat-x (claude)'
chk "a worktree tab is named for the worktree" 'tab rename w1:t1 tandem: feat-x'
REPO="$MAIN"

# A name set by hand is shown whole when it does not start with the role.
HERDR_BUILDER_NAME=bob; export HERDR_BUILDER_NAME
pair 3
chk "a builder named by hand shows that name" 'builder · bob (claude)'
chk "and the planner keeps its plain label" 'pane rename w1:p1 planner (claude)'
unset HERDR_BUILDER_NAME

printf '\npair-labels: passed %d, failed %d\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]

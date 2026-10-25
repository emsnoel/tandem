#!/usr/bin/env bash
# tandem-pair must launch the builder past every interactive gate Codex has.
#
# Hermetic: herdr is stubbed and CODEX_HOME is a temp dir. tandem-unpair runs
# this check while a builder is live, so it must need no server or panes.
set -uo pipefail
. "$(cd "$(dirname "$0")" && pwd)/lib/records.sh"
PAIR="${1:-$(cd "$(dirname "$0")/.." && pwd)/bin/tandem-pair}"
[ -x "$PAIR" ] || { echo "not executable: $PAIR" >&2; exit 2; }

PASS=0; FAIL=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }
chk()  { case "$OUT" in *"$2"*) ok "$1" ;; *) nope "$1 (missing: $2)" ;; esac; }
nchk() { case "$OUT" in *"$2"*) nope "$1 (unexpected: $2)" ;; *) ok "$1" ;; esac; }

TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
mkdir -p "$TMP/bin" "$TMP/codex" "$TMP/copilot" "$TMP/repo" "$TMP/home" "$TMP/nohome"
REPO_PHYS=$(cd "$TMP/repo" && pwd -P)
printf '{"projects":{"%s":{"hasTrustDialogAccepted":true}}}\n' "$REPO_PHYS" \
  > "$TMP/home/.claude.json"

# Build from this tree into $TMP/bin; test-staleness.sh checks the installed
# binary separately.
GOCACHE="$TMP/go-cache" GOMODCACHE="$TMP/go-mod-cache" \
  go build -o "$TMP/bin/tandem" ./cmd/tandem \
  || { echo "could not build tandem for the fixture" >&2; exit 2; }

# A planner-only session: one claude agent in a wide pane, so the split
# direction is fixed.
cat > "$TMP/bin/herdr" <<'STUB'
#!/usr/bin/env bash
case "$1 $2" in
  # agent_status is required by the facade and always present in real output.
  "agent list")
    # FAKE_RUNNING="name:kind ..." adds already-running agents beside the planner.
    printf '{"result":{"agents":[{"pane_id":"p1","agent":"claude","name":null,"cwd":"%s","agent_status":"idle"}' "$FAKE_CWD"
    for _a in ${FAKE_RUNNING:-}; do
      printf ',{"pane_id":"r-%s","agent":"%s","name":"%s","cwd":"%s","agent_status":"idle"}' "${_a%%:*}" "${_a##*:}" "${_a%%:*}" "$FAKE_CWD"
    done
    printf ']}}' ;;
  "pane layout") printf '{"result":{"layout":{"panes":[{"pane_id":"p1","rect":{"width":200,"height":50}}]}}}' ;;
  *) printf '{"error":{"code":"unexpected","message":"stub got: %s"}}' "$*" >&2; exit 1 ;;
esac
STUB
chmod +x "$TMP/bin/herdr"

# run_pair <env command...> [-- <tandem-pair flags...>]
run_pair() {
  local pre=()
  while (( $# )) && [[ "$1" != -- ]]; do pre+=("$1"); shift; done
  (( $# )) && shift
  OUT=$(PATH="$TMP/bin:$PATH" HERDR_ENV=1 HERDR_PANE_ID=p1 \
    HOME="$TMP/home" CODEX_HOME="$TMP/codex" COPILOT_HOME="$TMP/copilot" \
    FAKE_CWD="$TMP/repo" \
    "${pre[@]}" "$PAIR" --dry-run --cwd "$TMP/repo" ${1+"$@"} 2>&1); RC=$?
}

echo "--- launch flags: codex (default) ---"
run_pair env
chk  "renders an agent start line"          "agent start builder --kind codex"
chk  "uses the -- agent-args passthrough"   " -- "
chk  "disables approval prompts"            "--ask-for-approval never"
chk  "keeps the sandbox at workspace-write" "--sandbox workspace-write"
nchk "never drops the sandbox by default"   "--dangerously-bypass-approvals-and-sandbox"

echo "--- launch flags: opencode ---"
run_pair env HERDR_BUILDER_KIND=opencode HERDR_BUILDER_AUTONOMY=full
chk  "starts opencode"              "agent start builder --kind opencode"
chk  "auto-approves"                "--auto"
chk  "derives the build persona"    "--agent build"
nchk "never passes codex approval"  "--ask-for-approval"
nchk "never passes a codex sandbox" "--sandbox"

# Trust is in place so the copilot launch is asserted; the refusal is
# asserted separately below.
printf '{"trustedFolders":["%s"]}\n' "$REPO_PHYS" > "$TMP/copilot/config.json"

echo "--- launch flags: copilot ---"
run_pair env HERDR_BUILDER_KIND=copilot HERDR_BUILDER_AUTONOMY=auto HERDR_BUILDER_EFFORT=high
chk  "starts copilot"                 "agent start builder --kind copilot"
chk  "auto-approves tools"            "--allow-all-tools"
chk  "auto-approves urls"             "--allow-all-urls"
chk  "removes the ask_user stall"     "--no-ask-user"
chk  "starts a fresh session"         "--session-id"
chk  "passes the effort"              "--effort high"
nchk "never passes a codex sandbox"   "--sandbox"
chk  "auto-approves paths"            "--allow-all-paths"

echo "--- launch flags: claude ---"
run_pair env HERDR_BUILDER_KIND=claude HERDR_BUILDER_AUTONOMY=auto HERDR_BUILDER_MODEL=opus
chk  "starts claude"    "agent start builder --kind claude"
chk  "maps autonomy to Claude's auto mode" "--permission-mode auto"
nchk "never acceptEdits, which asks before every shell command" "acceptEdits"
chk  "passes the model" "--model opus"
nchk "never passes -s"  "--sandbox"

echo "--- a reviewer pane ---"
# The reviewer's launch line alone, so a leaked builder flag cannot hide.
reviewer_line() { printf '%s\n' "$OUT" | grep 'agent start reviewer' || true; }
# manifest <json> writes the repository's manifest; the reviewer comes from it.
manifest() { printf '%s\n' "$1" > "$TMP/repo/.tandem.json"; }
rm -f "$TMP/repo/.tandem.json"
run_pair env
nchk "no reviewer unless one is declared"     "agent start reviewer"
nchk "and no reviewer record"                 "would record reviewer"
chk  "pairing still succeeds"                 "pair ready"
case "$(printf '%s\n' "$OUT" | grep -c 'review and build will refuse')" in
  1) ok "exactly one warning that review and build will refuse" ;;
  *) nope "expected one 'review and build will refuse' warning: $OUT" ;;
esac
chk  "the warning names the reviewer"         "reviewer"
run_pair env HERDR_REVIEWER_KIND=codex
nchk "HERDR_REVIEWER_KIND alone starts no reviewer" "agent start reviewer"
chk  "and still warns"                        "review and build will refuse"

run_pair env HERDR_BUILDER_MODEL=gpt-5.4 HERDR_BUILDER_EFFORT=low -- \
  --reviewer claude --reviewer-model opus --reviewer-effort high
chk  "starts the reviewer with its own kind"  "agent start reviewer --kind claude"
chk  "keeps the builder as it was"            "agent start builder --kind codex"
chk  "splits the reviewer under the builder"  "--direction down"
chk  "names the reviewer's role in its pane"  "HERDR_ROLE=reviewer"
chk  "records the reviewer for the run"       'would record reviewer: {"kind":"claude","name":"reviewer","model":"opus","autonomy":"auto","model_measured":true}'
nchk "no warning when a reviewer is flagged"  "review and build will refuse"
case "$(reviewer_line)" in *"--model opus"*) ok "passes the reviewer's model" ;; *) nope "no reviewer model: $(reviewer_line)" ;; esac
case "$(reviewer_line)" in *"--effort high"*) ok "passes the reviewer's effort" ;; *) nope "no reviewer effort: $(reviewer_line)" ;; esac
case "$(reviewer_line)" in *gpt-5.4*|*low*) nope "the builder's model or effort reached the reviewer: $(reviewer_line)" ;; *) ok "the builder's model and effort stay the builder's" ;; esac
# With no model of its own, the reviewer must not inherit the builder's.
run_pair env HERDR_BUILDER_MODEL=gpt-5.4 HERDR_BUILDER_EFFORT=low -- --reviewer codex
chk  "starts a reviewer of the flagged kind"  "agent start reviewer --kind codex"
case "$(reviewer_line)" in *gpt-5.4*|*low*) nope "the builder's model or effort reached a reviewer with none: $(reviewer_line)" ;; *) ok "a reviewer with no model of its own does not take the builder's" ;; esac
chk  "a reviewer with no model records none, unmeasured" 'would record reviewer: {"kind":"codex","name":"reviewer","model":"","autonomy":"auto","model_measured":false}'
run_pair env -- --reviewer-model opus
chk  "a reviewer model needs a reviewer"      "need a reviewer"
nchk "and starts nothing"                     "agent start"
run_pair env HERDR_REVIEWER_NAME=builder -- --reviewer claude
chk  "the builder cannot be the reviewer"     "review its own work"
nchk "and nothing is started"                 "agent start"

echo "--- a reviewer declared in the manifest ---"
# A repository's own reviewer kind needs the operator's approval, like its builder's.
manifest '{"reviewer":{"kind":"codex","model":"gpt-6-astra"}}'
run_pair env
[ "$RC" -eq 22 ] && ok "an unapproved repo reviewer kind exits 22" || nope "exit $RC, wanted 22: $OUT"
chk  "and names the remedy"                   "--approve-kind"
nchk "and starts nothing"                     "agent start"
run_pair env -- --approve-kind codex
[ "$RC" -eq 0 ] && ok "approved, pairing succeeds" || nope "exit $RC: $OUT"
chk  "starts the declared reviewer"           "agent start reviewer --kind codex"
case "$(reviewer_line)" in *"--model gpt-6-astra"*) ok "launches with the declared model" ;; *) nope "no declared model: $(reviewer_line)" ;; esac
chk  "records the declared model as measured" 'would record reviewer: {"kind":"codex","name":"reviewer","model":"gpt-6-astra","autonomy":"auto","model_measured":true}'
chk  "says where each value came from"        "model gpt-6-astra (repo)"
run_pair env -- --reviewer-model other
case "$(reviewer_line)" in *"--model other"*) ok "--reviewer-model overrides the declared model" ;; *) nope "no override: $(reviewer_line)" ;; esac
nchk "and the declared one is gone"           "gpt-6-astra"
manifest '{"builder":{"kind":"copilot","model":"claude-sonnet-5.5"},"reviewer":{"kind":"codex"}}'
run_pair env -- --approve-kind copilot
chk  "a copilot builder keeps its model"      "agent start builder --kind copilot"
chk  "beside a codex reviewer"                "agent start reviewer --kind codex"
case "$(reviewer_line)" in *claude-sonnet-5.5*) nope "the builder's model reached the reviewer: $(reviewer_line)" ;; *) ok "the reviewer does not inherit the builder block's model" ;; esac
# One pairing approves a Copilot builder and a Codex reviewer together. A
# fresh HOME holds no earlier approval.
run_pair env HOME="$TMP/home-approve-one" -- --approve-kind copilot
[ "$RC" -eq 22 ] && ok "approving the builder's kind alone still refuses the reviewer's" || nope "exit $RC, wanted 22: $OUT"
run_pair env HOME="$TMP/home-approve-both" -- --approve-kind copilot --approve-kind codex
[ "$RC" -eq 0 ] && ok "--approve-kind given twice approves both kinds in one run" || nope "exit $RC: $OUT"
chk  "and starts the copilot builder"         "agent start builder --kind copilot"
chk  "and the codex reviewer"                 "agent start reviewer --kind codex"
manifest '{"reviewer":{"model":"x"}}'
run_pair env
[ "$RC" -ne 0 ] && ok "a reviewer block with no kind refuses" || nope "exit $RC: $OUT"
chk  "and names reviewer.kind"                "reviewer.kind"
nchk "and starts nothing"                     "agent start"
nchk "and records nothing"                    "would record reviewer"

echo "--- a reviewer that is already running ---"
manifest '{"reviewer":{"kind":"codex","model":"gpt-6-astra"}}'
MBX="$TMP/repo/.tandem-relay"; mkdir -p "$MBX"
write_reviewer "$MBX" codex reviewer gpt-6-astra
run_pair env FAKE_RUNNING="builder:codex reviewer:codex" -- --approve-kind codex
chk  "adopts the running reviewer"            "reviewer already present"
nchk "a matching declaration raises no unenforced notice" "declared reviewer model"
nchk "and starts no second one"               "agent start reviewer"
chk  "keeps the recorded model and its measurement" 'would record reviewer: {"kind":"codex","name":"reviewer","model":"gpt-6-astra","autonomy":"auto","model_measured":true}'
# The record says what is known about a running agent, not what the manifest
# declares now.
manifest '{"reviewer":{"kind":"codex","model":"changed-since"}}'
run_pair env FAKE_RUNNING="builder:codex reviewer:codex" -- --approve-kind codex
chk  "a changed declaration does not rewrite a running reviewer's model" '"model":"gpt-6-astra","autonomy":"auto","model_measured":true'
chk  "and says the declared model is unenforced" "declared reviewer model 'changed-since' is UNENFORCED"
rm -f "$MBX/reviewer.json"
run_pair env FAKE_RUNNING="builder:codex reviewer:codex" -- --approve-kind codex
chk  "a running reviewer with no record is not measured" 'would record reviewer: {"kind":"codex","name":"reviewer","model":"","autonomy":"auto","model_measured":false}'
write_reviewer "$MBX" claude reviewer opus
run_pair env FAKE_RUNNING="builder:codex reviewer:codex" -- --approve-kind codex
chk  "a record for another kind is not carried over" '"model":"","autonomy":"auto","model_measured":false'
rm -rf "$MBX" "$TMP/repo/.tandem.json"

echo "--- pre-launch gates ---"
run_pair env
chk  "reports the codex trust gate"            "project-trust"
chk  "says what it would write"                'trust_level = "trusted"'
run_pair env HERDR_BUILDER_KIND=opencode HERDR_BUILDER_AUTONOMY=full
nchk "no codex gate for an opencode builder"   "project-trust"
nchk "no hook warning for an opencode builder" "hook"
run_pair env HERDR_BUILDER_KIND=copilot HERDR_BUILDER_AUTONOMY=auto
nchk "no hook warning for a copilot builder"   "hook"
nchk "never writes codex trust for copilot"    'trust_level = "trusted"'

# An untrusted folder is trusted for the user, because HERDR_BUILDER_KIND is
# the user's choice. A repo-requested kind without approval is refused earlier
# with exit 22; internal/agent/trust covers that.
echo "--- a copilot builder gets its folder trusted ---"
rm -f "$TMP/copilot/config.json"
run_pair env HERDR_BUILDER_KIND=copilot HERDR_BUILDER_AUTONOMY=auto
chk  "reports the copilot trust gate"   "project-trust"
chk  "says what it would write"         "would add"
chk  "launches rather than refusing"    "agent start builder --kind copilot"
[ -e "$TMP/copilot/config.json" ] \
  && nope "a dry run wrote to copilot's config" \
  || ok   "a dry run writes nothing"

echo "--- a builder must not launch into a manual gate ---"
rc=0
OUT=$(PATH="$TMP/bin:$PATH" HERDR_ENV=1 HERDR_PANE_ID=p1 \
  HOME="$TMP/nohome" CODEX_HOME="$TMP/codex" FAKE_CWD="$TMP/repo" \
  env HERDR_BUILDER_KIND=claude HERDR_BUILDER_AUTONOMY=auto \
  "$PAIR" --dry-run --cwd "$TMP/repo" 2>&1) || rc=$?
[ "$rc" -eq 23 ] && ok "refuses exit 23" || nope "refuses exit 23 (got $rc)"
chk "names the remedy" "accept the trust dialog"
nchk "never renders a launch line" "agent start builder"

echo "--- fail closed ---"
run_pair env HERDR_BUILDER_KIND=opencode HERDR_BUILDER_AUTONOMY=readonly
chk "refuses a widening" "widening"
chk "names the remedy"   "allow_widen"

echo "--- legacy env still works ---"
run_pair env HERDR_BUILDER_SANDBOX=danger-full-access
chk "honours HERDR_BUILDER_SANDBOX" "--dangerously-bypass-approvals-and-sandbox"
chk "warns that it is deprecated"   "deprecated"

echo "--- adoption reports what it cannot enforce ---"
cat > "$TMP/bin/herdr" <<'STUB2'
#!/usr/bin/env bash
case "$1 $2" in
  "agent list")  printf '{"result":{"agents":[{"pane_id":"p1","agent":"claude","name":null,"cwd":"%s","agent_status":"idle"},{"pane_id":"p9","agent":"opencode","name":null,"cwd":"%s","agent_status":"idle"}]}}' "$FAKE_CWD" "$FAKE_CWD" ;;
  "pane layout") printf '{"result":{"layout":{"panes":[{"pane_id":"p1","rect":{"width":200,"height":50}}]}}}' ;;
  *) printf '{"error":{"code":"unexpected","message":"stub got: %s"}}' "$*" >&2; exit 1 ;;
esac
STUB2
chmod +x "$TMP/bin/herdr"
run_pair env HERDR_BUILDER_KIND=codex HERDR_BUILDER_MODEL=gpt-5.4
chk  "adopts the running agent whatever its kind" "adopting the running opencode agent"
chk  "names the axes it cannot enforce"           "UNENFORCED"
chk  "says how to apply them"                     "Restart the pane"
nchk "never adopts the planner pane"              "adopting the running claude agent"

echo "--- adoption stays beside the planner ---"
# A claude session in another tab is someone else's work. It must not become
# the builder, and --builder KIND must not adopt an agent of another kind.
cat > "$TMP/bin/herdr" <<'STUB3'
#!/usr/bin/env bash
case "$1 $2" in
  "agent list")  printf '{"result":{"agents":[{"pane_id":"p1","tab_id":"t1","agent":"claude","name":null,"cwd":"%s","agent_status":"idle"},{"pane_id":"p9","tab_id":"%s","agent":"%s","name":null,"cwd":"%s","agent_status":"idle"}]}}' "$FAKE_CWD" "$FAKE_TAB" "$FAKE_KIND" "$FAKE_CWD" ;;
  "pane layout") printf '{"result":{"layout":{"panes":[{"pane_id":"p1","rect":{"width":200,"height":50}}]}}}' ;;
  *) printf '{"error":{"code":"unexpected","message":"stub got: %s"}}' "$*" >&2; exit 1 ;;
esac
STUB3
chmod +x "$TMP/bin/herdr"
run_pair env FAKE_TAB=t2 FAKE_KIND=claude
nchk "does not adopt an agent in another tab"         "adopting the running"
run_pair env FAKE_TAB=t1 FAKE_KIND=claude -- --builder codex
nchk "--builder codex does not adopt a claude agent"  "adopting the running"
run_pair env FAKE_TAB=t1 FAKE_KIND=codex -- --builder codex
chk  "--builder codex adopts a codex agent beside the planner" "adopting the running codex agent"

echo "--- launch flags: tandem owns judgments ---"
# Judging never changes launch permissions or invokes a sandbox probe.
mkdir -p "$TMP/judgebin"
cat > "$TMP/judgebin/codex" <<STUB
#!/usr/bin/env bash
case "\$1" in
  sandbox)   printf '%s\n' "\$*" >> "$TMP/codex-sandbox.log"; exit 0 ;;
  --version) echo "codex-cli 0.155.1" ;;
  *)         exit 1 ;;
esac
STUB
chmod +x "$TMP/judgebin/codex"
JPATH="$TMP/judgebin:$TMP/bin:$PATH"
# Back to the planner-only session: the adoption cases left a running builder,
# which is never launched.
cat > "$TMP/bin/herdr" <<'STUB'
#!/usr/bin/env bash
case "$1 $2" in
  "agent list")
    # FAKE_RUNNING="name:kind ..." adds already-running agents beside the planner.
    printf '{"result":{"agents":[{"pane_id":"p1","agent":"claude","name":null,"cwd":"%s","agent_status":"idle"}' "$FAKE_CWD"
    for _a in ${FAKE_RUNNING:-}; do
      printf ',{"pane_id":"r-%s","agent":"%s","name":"%s","cwd":"%s","agent_status":"idle"}' "${_a%%:*}" "${_a##*:}" "${_a%%:*}" "$FAKE_CWD"
    done
    printf ']}}' ;;
  "pane layout") printf '{"result":{"layout":{"panes":[{"pane_id":"p1","rect":{"width":200,"height":50}}]}}}' ;;
  *) printf '{"error":{"code":"unexpected","message":"stub got: %s"}}' "$*" >&2; exit 1 ;;
esac
STUB
chmod +x "$TMP/bin/herdr"

run_pair env PATH="$JPATH" HERDR_JUDGE=1 TYPESAFE_API_KEY=ts-test
chk  "judging keeps the ordinary sandbox"         "--sandbox workspace-write"
nchk "judging adds no permissions profile"        "permissions.tandem-builder"
nchk "judging sets no builder environment"         "shell_environment_policy"
nchk "the builder record has no network field"    '"network_allow"'
[ ! -s "$TMP/codex-sandbox.log" ] && ok "a dry run never probes the network" \
  || nope "a dry run probed the network: $(cat "$TMP/codex-sandbox.log")"

# No key: the launch is unchanged.
run_pair env -u TYPESAFE_API_KEY PATH="$JPATH" HERDR_JUDGE=1
chk  "without a key the sandbox is unchanged"    "--sandbox workspace-write"
nchk "and the network is not widened"            "permissions.tandem-builder"
nchk "and nothing is set in the builder's env"   "shell_environment_policy"
nchk "and the builder record has no network field" '"network_allow"'

# An adopted builder has the same identity format, with no network field.
cat > "$TMP/bin/herdr" <<'STUB'
#!/usr/bin/env bash
case "$1 $2" in
  "agent list")  printf '{"result":{"agents":[{"pane_id":"p1","agent":"claude","name":null,"cwd":"%s","agent_status":"idle"},{"pane_id":"p9","agent":"codex","name":null,"cwd":"%s","agent_status":"idle"}]}}' "$FAKE_CWD" "$FAKE_CWD" ;;
  "pane layout") printf '{"result":{"layout":{"panes":[{"pane_id":"p1","rect":{"width":200,"height":50}}]}}}' ;;
  *) printf '{"error":{"code":"unexpected","message":"stub got: %s"}}' "$*" >&2; exit 1 ;;
esac
STUB
chmod +x "$TMP/bin/herdr"
run_pair env PATH="$JPATH" HERDR_JUDGE=1 TYPESAFE_API_KEY=ts-test
chk  "adopts the running codex builder"          "adopting the running codex agent"
nchk "an adopted builder's record has no network field" '"network_allow"'

echo; printf 'launch-flags: passed %d, failed %d\n' "$PASS" "$FAIL"; [ "$FAIL" -eq 0 ]

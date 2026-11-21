#!/usr/bin/env bash
# tandem-pair must launch the builder past every interactive gate Codex has.
#
# Hermetic: herdr is stubbed and CODEX_HOME is a temp dir. tandem-unpair runs
# this check while a builder is live, so it must need no server or panes.
set -uo pipefail
. "$(cd "$(dirname "$0")" && pwd)/lib/records.sh"
ROOT=$(cd "$(dirname "$0")/.." && pwd)
PAIR="${1:-$ROOT/bin/tandem-pair}"
. "$ROOT/tests/lib/launch-fixture.sh"

echo "--- launch flags: codex (default) ---"
run_pair env
chk  "renders an agent start line"          "agent start builder --kind codex"
chk  "uses the -- agent-args passthrough"   " -- "
chk  "disables approval prompts"            "--ask-for-approval never"
chk  "keeps the sandbox at workspace-write" "--sandbox workspace-write"
nchk "never drops the sandbox by default"   "--dangerously-bypass-approvals-and-sandbox"

echo "--- launch flags: opencode is refused ---"
run_pair env -- --builder opencode
[ "$RC" -eq 20 ] && ok "refuses with exit 20" || nope "refuses with exit 20 (got $RC)"
chk  "names the supported kinds"    "supported kinds: agy, claude, codex, copilot"
nchk "never renders a launch line"  "agent start builder"

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
chk  "passes the effort"              "--reasoning-effort high"
nchk "never passes a codex sandbox"   "--sandbox"
chk  "auto-approves paths"            "--allow-all-paths"

echo "--- launch flags: copilot with --builder-model (#92) ---"
run_pair env -- --builder copilot --builder-model claude-sonnet-5.5
chk  "starts copilot with the model"  "--model claude-sonnet-5.5"
chk  "records the builder model"      'would record builder identity: {"kind":"copilot","autonomy":"auto","model":"claude-sonnet-5.5","adopted":false}'

echo "--- launch flags: claude ---"
run_pair env HERDR_BUILDER_KIND=claude HERDR_BUILDER_AUTONOMY=auto HERDR_BUILDER_MODEL=opus
chk  "starts claude"    "agent start builder --kind claude"
chk  "maps autonomy to Claude's auto mode" "--permission-mode auto"
nchk "never acceptEdits, which asks before every shell command" "acceptEdits"
chk  "passes the model" "--model opus"
nchk "never passes -s"  "--sandbox"
# A lean builder loads none of the user's plugins, hooks or MCP servers (#63),
# and keeps Herdr's session hook.
chk  "leaves out user settings"       "--setting-sources project,local"
chk  "keeps only Herdr's session hook" "hooks/herdr-agent-state.sh"
chk  "leaves out user MCP servers"    "--strict-mcp-config"
mkdir -p "$TMP/home/.config/herdr"
printf '{"adapters":{"claude":{"isolation":"none"}}}\n' > "$TMP/home/.config/herdr/tandem.json"
run_pair env HERDR_BUILDER_KIND=claude HERDR_BUILDER_AUTONOMY=auto
chk  "isolation none still starts claude" "agent start builder --kind claude"
nchk "isolation none loads user settings"  "--setting-sources"
nchk "isolation none loads user MCP servers" "--strict-mcp-config"
rm -f "$TMP/home/.config/herdr/tandem.json"

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
case "$(reviewer_line)" in *"--setting-sources project,local"*"--strict-mcp-config"*) ok "a claude reviewer is lean too" ;; *) nope "the claude reviewer is not lean: $(reviewer_line)" ;; esac
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

echo "--- a sandbox grant reaches codex (#85) ---"
# A Gradle check needs ~/.gradle and loopback sockets. The user grants both in
# their own config, and the builder and the reviewer of that kind get them.
printf '{"adapters":{"codex":{"sandbox":{"writable_dirs":["~/.gradle"],"network":true}}}}\n' > "$TMP/grant.json"
run_pair env HERDR_TANDEM_CONFIG="$TMP/grant.json" -- --reviewer codex
for role in builder reviewer; do
  line=$(printf '%s\n' "$OUT" | grep "agent start $role" || true)
  case "$line" in *"--add-dir $TMP/home/.gradle"*) ok "the $role may write ~/.gradle" ;; *) nope "no ~/.gradle grant for the $role: $line" ;; esac
  case "$line" in *"sandbox_workspace_write.network_access=true"*) ok "the $role may open sockets" ;; *) nope "no network grant for the $role: $line" ;; esac
done
run_pair env -- --reviewer codex
nchk "no grant unless the user gives one"     "--add-dir"
nchk "and no network either"                  "network_access"

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
run_pair env FAKE_RUNNING="builder:codex reviewer:codex" -- --approve-kind codex --reviewer-model flag-model
chk  "a model flag on a running reviewer is named" "declared reviewer model 'flag-model' is UNENFORCED"
nchk "and is reported once"                     "reviewer-effort are UNENFORCED"
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
  "$PAIR" --dry-run --skip-doctor --cwd "$TMP/repo" 2>&1) || rc=$?
[ "$rc" -eq 23 ] && ok "refuses exit 23" || nope "refuses exit 23 (got $rc)"
chk "names the remedy" "accept the trust dialog"
nchk "never renders a launch line" "agent start builder"

echo "--- fail closed ---"
run_pair env HERDR_BUILDER_KIND=codex HERDR_BUILDER_EFFORT=max
chk "refuses a narrowing" "allow_degrade"

echo "--- legacy env still works ---"
run_pair env HERDR_BUILDER_SANDBOX=danger-full-access
chk "honours HERDR_BUILDER_SANDBOX" "--dangerously-bypass-approvals-and-sandbox"
chk "warns that it is deprecated"   "deprecated"


echo; printf 'launch-flags: passed %d, failed %d\n' "$PASS" "$FAIL"; [ "$FAIL" -eq 0 ]

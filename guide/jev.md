# Optional Jev features

## Staffing advice (optional, off by default)

With `HERDR_DECISION=1` and a `TYPESAFE_API_KEY` in the environment, `tandem-handoff`
classifies the plan before sending it and adds a short, explicitly advisory block to the
builder's prompt: what shape the work is, whether it looks parallelisable, and — for
builders that can select a per-subagent capability — the lowest level to delegate it at.

The builder may ignore all of it. Nothing here can change a review verdict, widen
authority, alter a launch argument, or affect teardown. `internal/gate` is forbidden from
importing `internal/decision` by a test, so a judgement cannot reach a gate even by
accident.

```bash
HERDR_DECISION=1 tandem-handoff                   # ask for advice on this handoff
tandem decision assess --plan P --root R --dry-run  # show what would be sent, send nothing
```

Without the key, or if the call fails or times out, nothing is added and the prompt is
exactly what it would have been — `tests/test-decision.sh` asserts that byte-for-byte
against the real script. The credential is read from the environment only, never from
`.tandem.json`, which is cloned with the repository.

**The handoff says which it was.** Because every non-advice path leaves the prompt
untouched, "Jev was never asked" and "Jev answered and chose not to advise" look identical
from the prompt. So `tandem-handoff` prints one line either way, outside the prompt:

```
staffing advice: added to the launch prompt (Jev).
staffing advice: none (Jev answered but was not confident enough about this plan to advise).
staffing advice: off (HERDR_DECISION is not 1).
WARNING staffing advice: HERDR_DECISION=1 but TYPESAFE_API_KEY is not set; Jev was not asked.
WARNING staffing advice: the Jev call failed (<reason>); none added.
```

"None" is the common answer for a plan Jev cannot classify confidently: by design, an
uncertain assessment adds nothing rather than a guess. `tandem decision assess --status FILE`
is what reports it. Note the shell a handoff runs in: a planner started before you exported
`HERDR_DECISION` does not have it, and the line will say `off`.

Per-subagent capability selection is expressible by `claude` and `codex` builders.
`claude` agent definitions carry a default `model:` that the dispatch-time parameter
overrides. `codex` was verified against codex-cli 0.155.1 (`multi_agent_version` v2) by
reading a real session's rollout log: `spawn_agent` takes `model` and `reasoning_effort`
as well as `{task_name, fork_turns, message}`, and honours both — a parent running
`gpt-5.6-sol` at medium spawned children that ran `gpt-5.6-luna` at medium and
`gpt-5.6-terra` at high. Project-scoped `.codex/agents/*.toml` is still not read, so a
named persona cannot be selected this way, only a capability.

`copilot` and `opencode` are unverified, and unverified means "cannot express it" rather
than "probably fine" — for those the advisory still describes the shape of the work, it
just does not name a level.

> An earlier version of this section said the opposite of the `codex` paragraph above,
> citing the same CLI version. It was wrong when written rather than overtaken by a
> release. `internal/agent/subagent_test.go` now pins the claim to the evidence.

## Review triage (optional, off by default)

Set `HERDR_JUDGE=1` when running `tandem-handoff` to enable task judgments.
Handoff stores the switch as `judge` in `.tandem-relay/build/build.json`; later
handoffs and the bridge use that stored value. Enabling it needs a
`TYPESAFE_API_KEY` from an active Jev subscription in Tandem's environment.
It works with every builder kind and leaves the builder's launch permissions unchanged.

Tandem decides when a task gets a review. The builder implements the task and hands
it back. `tandem build advance` runs the project's check, then asks Jev about the
files that task changed, including new files, with Tandem's actual check result.
The diff, brief and check output are redacted before the call, and the diff is
size-capped with truncation recorded. The builder needs no Jev network scope or
credential to perform the task.

During calibration, every judged task gets a review round, even when Jev returns
`clean`. The builder never reviews. `tandem build start` refuses with exit 23 when
`.tandem-relay/reviewer.json` records no reviewer pane, and the build records that
pane's name. The bridge types the review prompt into the reviewer pane. The reviewer
reads the brief and `git diff HEAD`, edits nothing, writes its findings and runs
`tandem-handback --task N --review --role NAME --verdict clean|changes --report FILE`,
where the prompt fills in NAME, the reviewer pane's recorded name. `tandem-handback`
refuses a review from any other pane, by the rule in
[Which pane is the reviewer](reviews.md#which-pane-is-the-reviewer). `tandem build advance` refuses a review handback from any other pane,
or from a pane running the builder's conversation, and leaves the task in review. A
`clean` verdict accepts the task. With `changes`, the bridge sends the findings to the
builder's own conversation (`fix N MSGFILE`), the builder fixes them and hands back as
before, and the next round opens. After 3 rounds of `changes` the task stops with
`review_rounds`. A `changes` verdict on a task already over its budget stops with
`budget`. In both cases the findings are not sent. Tandem compares the tree before and
after each review: a reviewer that changed the task's files voids the round and stops
with `review_edited_tree`, and any change reruns the check.
A missing key, provider failure or malformed answer produces an `unavailable`
record with its reason and still gets a review. A delivery failure stops the task.

Each judgment is recorded to `.tandem-log/judge-<id>.json` with its answers, contract,
policy and model versions, the true check result, and whether it *would* have
advised skipping. Reviewer facts record whether the reviewer pane returned a
verdict, and whether the review changed the outcome: it asked for changes, or
edited the tree. These measurements provide the evidence needed before skips
can be enabled. A red check never qualifies for a skip.

Skipping also requires `HERDR_TRIAGE_SKIP=1` in the bridge's environment,
separately from `HERDR_JUDGE=1` at handoff. It is never read from `.tandem.json`.
`tandem triage report` must show at least 30 reviewed would-skip pairs, at least
10 pairs where the reviewer changed the tree, and zero misses under the current
contract, policy and model. One miss keeps that gate closed until a version changes.
Only a confident skip with a check that exited 0 and no stuck handback qualifies.
Confident means implements and evidence at least .90, scope creep and destructive
at most .10, and review priority at most .25. The diff must also be untruncated and
known to be the attempt's complete change. The current build loop reads a
HEAD-relative diff of a shared worktree, which cannot show that, so it still
records `would_skip` but never acts on it. Every row names exactly what it is
evidence about (`triage-evidence/v1`): a hash of the skip policy and its thresholds, a
hash of the judge's model and questions, a hash of the reviewer's kind, model and review
prompt, and the hash of the exact input the judge was shown. A change to any of them is a
new cohort: the report counts only rows of the current cohort and lists every other row
under the reason it was left out (`legacy_record`, `other_cohort`, `awaiting_review`,
`not_reviewed`, `judge_unavailable`, `incomplete_identity`, `tree_unknown`,
`tree_mismatch`, `truncated_input`). A pair needs the judgment and the review to be of
the same tree. A row that cannot be read, names another job than its file, or refers to
kept input that is missing or does not match its hash makes the evidence unhealthy and
closes the gate; it is counted, never dropped.
A deterministic one in five, selected by SHA-256 of the record ID and the policy hash,
read as a number, modulo 5, still gets a review to detect new misses; the choice is
settled when the judgment is recorded, before and independently of the verdict.
Skipped tasks retain null reviewer facts and never count as pairs. The report states an
upper bound on the true miss rate, and the rule-of-three caveat still applies: zero
misses in 30 allows a true miss rate near 10%, and reaching the minimum count is not a
proof of safety. Skip stays off by default whatever the report says; live calibration
and an explicit release decision are separate gates.

The exact input the judge was shown (the redacted brief, diff and check output, and the
versions it was shown under) is kept in `.tandem-log/judge-input-<hash>.json`, mode 0600,
named by its hash, so a judgment can be audited later. It is the text already sent to the
provider; keep `.tandem-log` out of version control.

With the stored switch off, Jev is never asked and the reviewer pane reviews every
task. Handoff prints whether Tandem calls Jev or why triage is off.
`tests/test-handoff-jev-status.sh` and `tests/test-handoff-reset.sh` cover those
instructions and their delivery.

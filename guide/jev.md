# Optional Jev features

Both features on this page are off by default. Each one calls Jev, a TypeSafe model, and
needs a `TYPESAFE_API_KEY` in the environment.

## Staffing advice

With `HERDR_DECISION=1` and a `TYPESAFE_API_KEY` in the environment, `tandem-handoff` asks
Jev to classify the plan before sending it. It then adds a short block to the builder's
prompt, marked as advice. The block says what kind of work it is and whether it looks like
it can run in parallel. For builders that can choose a model per subagent, it also names
the lowest level to hand the work to.

The builder may ignore all of it. The advice can't change a review verdict, widen
authority, change a launch argument or affect teardown. A test forbids `internal/gate`
from importing `internal/decision`, so a judgement can't reach a gate even by accident.

```bash
HERDR_DECISION=1 tandem-handoff                     # ask for advice on this handoff
tandem decision assess --plan P --root R --dry-run  # show what would be sent, and send nothing
```

Without the key, or if the call fails or times out, tandem adds nothing, and the prompt is
exactly what it would have been. `tests/test-decision.sh` checks that byte for byte against
the real script. tandem reads the key only from the environment, never from `.tandem.json`,
because `.tandem.json` is cloned with the repository.

**The handoff says what happened.** The prompt looks the same whether nobody asked Jev, or
Jev answered and chose not to advise. So `tandem-handoff` prints one line either way,
outside the prompt:

```
staffing advice: added to the launch prompt (Jev).
staffing advice: none (Jev answered but was not confident enough about this plan to advise).
staffing advice: off (HERDR_DECISION is not 1).
WARNING staffing advice: HERDR_DECISION=1 but TYPESAFE_API_KEY is not set; Jev was not asked.
WARNING staffing advice: the Jev call failed (<reason>); none added.
```

"None" is the usual answer for a plan Jev can't classify with confidence. An uncertain
assessment adds nothing, on purpose, instead of a guess. `tandem decision assess --status FILE`
reports it. Check which shell the handoff runs in. A planner started before you exported
`HERDR_DECISION` doesn't have the variable, and the line will say `off`.

`claude` and `codex` builders can choose a model for each subagent. A `claude` agent
definition has a default `model:`, and the parameter passed when the subagent starts
overrides it. For `codex`, we checked codex-cli 0.155.1 (`multi_agent_version` v2) by
reading a real session's rollout log. `spawn_agent` takes `model` and `reasoning_effort`,
as well as `{task_name, fork_turns, message}`, and uses both. A parent running `gpt-5.6-sol`
at medium started children that ran `gpt-5.6-luna` at medium and `gpt-5.6-terra` at high.
Codex still doesn't read project-level `.codex/agents/*.toml`, so the advice can choose a
model level, but not a named agent definition.

Nobody has checked `copilot` or `opencode`, so tandem treats them as unable to choose. For
those builders the advice still describes the work, but doesn't name a level.

> An earlier version of this section said the opposite about `codex`, citing the same CLI
> version. It was wrong when written, not made wrong by a later release.
> `internal/agent/subagent_test.go` now ties the claim to the evidence.

## Review triage

Set `HERDR_JUDGE=1` when you run `tandem-handoff` to turn on task judgments. The handoff
stores the switch as `judge` in `.tandem-relay/build/build.json`, and later handoffs and the
bridge use that stored value. It needs a `TYPESAFE_API_KEY` from an active Jev subscription
in tandem's environment. It works with every builder kind and doesn't change the builder's
launch permissions.

tandem decides when a task gets a review. The builder does the task and hands it back.
`tandem build advance` runs the project's check, then asks Jev about the files the task
changed, including new files, along with tandem's actual check result. tandem redacts the
diff, the brief and the check output before the call. It also caps the diff's size, and
records any truncation. The builder needs no Jev network access or key to do the task.

### How a judged task is reviewed

For now, while tandem collects evidence, every judged task gets a review round, even when
Jev returns `clean`. The builder never reviews. `tandem build start` refuses with exit 23
when `.tandem-relay/reviewer.json` records no reviewer pane, and the build records that
pane's name. Each round goes like this:

1. The bridge types the review prompt into the reviewer pane.
2. The reviewer reads the brief and `git diff HEAD`, edits nothing, and writes its
   findings.
3. The reviewer runs
   `tandem-handback --task N --review --role NAME --verdict clean|changes --report FILE`.
   The prompt fills in NAME, the reviewer pane's recorded name.

`tandem-handback` refuses a review from any other pane, by the rule in
[Which pane is the reviewer](reviews.md#which-pane-is-the-reviewer). `tandem build advance`
also refuses a review handback from any other pane, or from a pane running the builder's
conversation, and leaves the task in review.

- A `clean` verdict accepts the task.
- With `changes`, the bridge sends the findings to the builder's own conversation
  (`fix N MSGFILE`). The builder fixes them and hands back as before, and the next round
  starts.
- After 3 rounds of `changes`, the task stops with `review_rounds`.
- A `changes` verdict on a task already over its budget stops with `budget`.

In the last two cases, tandem doesn't send the findings. tandem compares the tree before
and after each review. If the reviewer changed the task's files, the round doesn't count
and the task stops with `review_edited_tree`. Any change also runs the check again.

A missing key, a provider failure or a malformed answer produces an `unavailable` record
with its reason, and the task still gets a review. A delivery failure stops the task.

### What tandem records

tandem records each judgment to `.tandem-log/judge-<id>.json`. The record holds Jev's
answers, the contract, policy and model versions, the real check result, and whether Jev
would have advised skipping the review. Reviewer facts record whether the reviewer pane
returned a verdict, and whether the review changed the outcome by asking for changes or
editing the tree. tandem needs this evidence before it can allow skips. A failed check
never qualifies for a skip.

tandem keeps the exact input Jev saw in `.tandem-log/judge-input-<hash>.json`, with mode
0600, named by its hash, so you can audit a judgment later. That input is the redacted
brief, diff and check output, and the versions it was shown under. It is text tandem
already sent to the provider. Keep `.tandem-log` out of version control.

### When a review can be skipped

Skipping needs `HERDR_TRIAGE_SKIP=1` in the bridge's environment, as well as
`HERDR_JUDGE=1` at handoff. tandem never reads it from `.tandem.json`. `tandem triage report`
must also show all of these, under the current contract, policy and model:

- at least 30 reviewed pairs where Jev would have skipped;
- at least 10 pairs where the reviewer changed the tree;
- no misses. One miss keeps skipping off until a version changes.

A task qualifies for a skip only when all of these are true:

- Jev is confident. "Implements" and "evidence" score at least .90, "scope creep" and
  "destructive" at most .10, and "review priority" at most .25.
- The check exited 0, and no handback was stuck.
- The diff is untruncated, and tandem knows it is the attempt's whole change.

The current build loop reads a diff against HEAD in a shared worktree, which can't show
that last point. So tandem still records `would_skip`, but never acts on it.

Every row names exactly what it is evidence about (`triage-evidence/v1`), with four hashes:

- the skip policy and its thresholds;
- the judge's model and questions;
- the reviewer's kind, model and review prompt;
- the exact input the judge saw.

A change to any of them starts a new group of rows. The report counts only rows in the
current group, and lists every other row under the reason it left the row out:
`legacy_record`, `other_cohort`, `awaiting_review`, `not_reviewed`, `judge_unavailable`,
`incomplete_identity`, `tree_unknown`, `tree_mismatch` or `truncated_input`. A pair counts
only when the judgment and the review are of the same tree.

Some rows make the evidence unhealthy, which turns skipping off. These are rows tandem
can't read, rows that name a different job from their file, and rows whose kept input is
missing or doesn't match its hash. The report counts them, and never drops them.

One task in five still gets a review, to catch new misses. tandem picks it with a fixed
rule: the SHA-256 of the record ID and the policy hash, read as a number, modulo 5. It
settles the choice when it records the judgment, before the verdict and without looking at
it. Skipped tasks keep empty reviewer facts and never count as pairs.

The report states an upper bound on the true miss rate. Zero misses in 30 still allows a
true miss rate near 10%, by the rule of three, so reaching the minimum count doesn't prove
skipping is safe. Skipping stays off by default whatever the report says. Live testing and
a deliberate release decision are separate steps.

With the stored switch off, tandem never asks Jev, and the reviewer pane reviews every
task. The handoff prints whether tandem calls Jev, or why triage is off.
`tests/test-handoff-jev-status.sh` and `tests/test-handoff-reset.sh` test those messages
and their delivery.

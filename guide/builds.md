# Builds

## Per-task builds

tandem runs a plan one task per conversation. Before that, one conversation held every
task, and that was the largest cost left once reviews and builds each got a fresh
conversation. On 2026-09-28, an 8-task build read the whole 2,229-line plan, the
conventions file (which Codex had already loaded) and three plan-execution skills before
task 1. That was 64k tokens of context before any edit. Each task's reads then stayed in
the context for the next task: task 1 began at 93k tokens, and task 5 at 208k. The build
spent 15.5M input tokens, and stopped in task 5 when Codex's 5-hour usage limit ran out.
Replayed with a fresh conversation per task, the same work costs about 4.4M.

The steps below describe the legacy engine, which is still the default. A durable run
splits the plan the same way, but a controller sends each task, runs the check and records
the review. The planner then signs the run off on its stored evidence, not on a report. See
[Build engines](build-engines.md) and [Durable state](durable-state.md).

1. `tandem-handoff` splits the plan at its `Task N` headings (`tandem build start`). Each
   brief in `.tandem-relay/build/task-N.md` holds the plan's title, its opening section
   (goal, architecture and constraints, without any `REQUIRED SUB-SKILL` line) and one
   task. A plan with no task headings is one task. Writing the briefs uses no model tokens.
2. The launch prompt names the task, its brief, and a ledger of the tasks before it. It
   doesn't ask the builder to read the whole plan. It asks for the conventions file only
   when the builder's CLI doesn't load it already. Codex loads `AGENTS.md`, and Claude Code
   loads `CLAUDE.md`.
3. The builder does the task, runs the check through `tandem check`, and runs
   `tandem-handback --task N --report FILE`, adding `--stuck` if it couldn't finish. That
   handback always goes through the mailbox to the relay bridge, never to the planner.
4. The bridge runs `tandem build advance`. It waits for the builder's turn to end and
   records the files the task changed, and what the conversation and any subagents cost.
   It runs the check itself, outside the sandbox, and decides. For a passing task, it sends
   the next task with `tandem-handoff --continue`. That resets the builder and runs the
   review gate again, so if the plan changes mid-build, the loop stops instead of building
   half of each version.
5. tandem wakes the planner once, when the build finishes or stops, with one report in
   `.tandem-relay/reports/`. `tandem-unpair --verified` signs it off as before.

Both engines need a reviewer pane. A legacy `tandem build start` refuses with exit 23 when
`reviewer.json` records none, and the build records that pane's name. `tandem-handoff`
refuses a durable start in the same case, with the same fix. Every legacy task gets a review
round in the reviewer pane. The launch prompt tells the builder not to review the task and
not to start a reviewer subagent. Only [review triage](jev.md#review-triage), when it is on,
can skip a round, and then nothing reviews that task.

## What stops a build

The planner's message names the reason and the fix:

| Stop | Why | To carry on |
| --- | --- | --- |
| `stuck` | The builder said it couldn't finish | Run `tandem-ask builder "..."` and the builder hands back task N again. Or run `tandem-handoff` to send task N again in a fresh conversation. Or, if the task is done, run `tandem build accept --task N --because "why"` and then `tandem-handoff` |
| `red` | The check tandem ran failed | The same as `stuck`. Handing back with nothing changed runs the same check, which stops the build again |
| `budget` | The task's input tokens passed `task_input_budget`. A reviewer's `changes` verdict on a task over its budget also stops here, and tandem sends no fix | Find out why with `tandem build status`, then `tandem-handoff` continues with the next task |
| `handoff` | tandem couldn't send the next task, because the plan changed or a dialog is open | Fix it, then run `tandem-handoff` |
| `unsettled` | The builder never finished its turn after handing back | Check its pane, then run `tandem-handoff` |
| `review_edited_tree` | The reviewer changed or added a file in the task while it reviewed, so its review doesn't count | Read `git diff`. Run `tandem build accept --task N --because "why"`, or undo the edits and run `tandem-handoff` |
| `review_rounds` | The reviewer still asked for changes after 3 rounds | Read its findings. Run `tandem-ask builder "..."` to have the builder fix them and hand back again, or run `tandem build accept --task N --because "why"` |

`task_input_budget` in `.tandem.json` sets each task's limit, and defaults to 3,000,000
input tokens. 0 turns it off. The most expensive task of that 8-task build, replayed in a
fresh conversation, came to 1.65M. So the default leaves room for a big task, and still stops
a missed reset or a runaway task long before a usage window runs out. tandem measures it
from Codex's own session logs. Other builder kinds report "not measured", and the budget
never stops them.

`tandem build status` prints the ledger: each task's status, check result, files, calls,
input tokens and largest context. `tandem check [-- ARGS]` runs the check, writes its output
to a log under `.tandem-relay/build/checks/`, and prints the exit code, the lines that name a
failure, and the last few lines. Before this, two raw Jest runs put about 80k characters into
one build's context on 2026-09-28.

We measured a live run on 2026-09-28, with codex-cli 0.158.0 and the eval fixture's 3-task
plan:

- Each task's first call used 22.5k to 23k tokens, the least a fresh conversation costs.
- No task read the plan or `AGENTS.md`.
- The bridge sent tasks 2 and 3 and finished without a person.
- The report passed `tandem-unpair`'s gate.

On a plan that small, one conversation never grows, so splitting it costs more: about 1.2M
tokens against 1.0M, not counting subagents. Each task pays the 22k starting cost on every
call. Splitting pays off when tasks would carry real file reads and test output forward,
as in the 8-task build above.

## A fresh conversation for each task

Before tandem required a reviewer pane, reviews and the build ran in the same builder pane,
so the build carried every review round with it. On 2026-09-25, a build began with 141k
tokens of context, against 26k for the first review. Its 46 calls cost 7.8M input tokens,
three quarters of the builder's total. Reviews now run in the reviewer pane, but the
builder's conversation can still hold an earlier build or a `tandem-ask` exchange, and the
build needs none of that history.

So just before sending the launch prompt, `tandem-handoff` runs `tandem agent reset`. It
types the builder CLI's own new-conversation command into the pane, `/new` for Codex or
`/clear` for Claude Code, and waits for the pane to settle. It leaves the pane, its process
and `builder.json` alone, so the review still stands.

**A reset has to be visible. Waiting is not proof.** Claude Code reports a new session id
as soon as it resets. For Codex, the sign is the new conversation starting, a change Herdr
counts in `state_change_seq` for that pane alone. The reset then reports `sent`. For
Claude, after delivery `tandem-handoff` asks `tandem agent session --not <old id>` for the
id the build runs in. If the id never changed, you get a warning that the build may still
hold the review, and tandem records nothing.

**tandem never reads a Codex pane's conversation from Herdr.** Codex runs every
conversation on one shared app-server process. The first Codex TUI on the machine starts
it, and every later one joins it. Herdr's Codex hook runs inside that process and reads
the pane id from its environment. So it credits every Codex conversation to the pane that
started the process, and never gives another Codex pane one. A process started outside
Herdr credits no pane (measured on 2026-10-12, codex-cli 0.162.1). So a Codex pane with no
session in Herdr may hold any number of turns. tandem resets it like any other pane, and
never skips it as empty.

A Codex task's conversation is the one whose log holds the task's nonce. tandem uses that
log for the task's usage, and to judge a turn that ended at once. A legacy prompt has no
nonce. So for any pane whose conversation Herdr doesn't name, `tandem agent prompt --as`
adds a last line to the prompt, `(tandem delivery <id>)`, and finds the turn's
conversation by that line.

**Codex 0.157 asks where the new conversation should run.** Its `/new` opens a native
picker ("Current checkout" or "New worktree") that Herdr can't see. The pane stays `idle`
and nothing changes. So when a reset command changes nothing for two seconds,
`tandem agent reset` presses the builder kind's confirm key once and waits again. For Codex
that is Enter, which picks the highlighted "Current checkout". If still nothing changes,
the reset is `unconfirmed`. `tandem-handoff` and `tandem-review` then send nothing, exit 7,
and name the pane. Typing into that screen lost a build prompt on 2026-09-28 (F21 in
`guide/design.md`).

**tandem never resets a conversation that holds a build task still in progress.** It
records that conversation's id in `.tandem-relay/build-session`. A later handoff into the
same session leaves it alone, because the planner's `tandem-ask` follow-ups rely on it.
When a task is checked and done, `tandem build advance` clears that record. The ledger now
holds what later tasks need, so the next task's handoff resets as usual.

**tandem resets a Copilot builder with `/new`, and confirms it from Copilot's own log.**
After `/new`, Herdr keeps reporting the `--session-id` the pane started with, and its
change counter doesn't move, so Herdr can't show the reset. Copilot writes
`Registering foreground session: <id>` to its process log,
`~/.copilot/logs/process-<ms>-<pid>.log`, as soon as `/new` runs. `tandem agent reset` finds
the pane's Copilot process with `herdr pane process-info`, and counts the reset only when
that log names a conversation opened after the command. Nothing else counts. A log line in
any other shape leaves the reset `unconfirmed`, and tandem doesn't send the task. For the
same reason, tandem never takes a Copilot pane's conversation id from Herdr. A task's
conversation is the one its reset named, or the one whose prompt holds the task's nonce in
`~/.copilot/session-state`.

**tandem doesn't reset an opencode builder.** Its `/new` does empty the conversation, but
nothing shows that until the next prompt. Herdr never reports an opencode session, its
counter doesn't move, and opencode writes no session, log line or state file before that
prompt. tandem doesn't claim a reset nobody can confirm (F18 in `guide/design.md`).

A skipped reset never blocks a build. For an unsupported kind, or a builder that is still
working or at a dialog before the reset, tandem prints one warning and delivers into the
existing conversation. Only a reset that tandem typed and couldn't confirm stops the
handoff, as described above. A Claude or Copilot builder with no session id yet hasn't
taken a turn since `tandem-pair`, so there is nothing to reset and nothing to warn about.

tandem treats a builder that Herdr reports as `done`, a finished turn nobody has looked at
yet, as settled, the same as `idle`. Treating it as busy let a build carry its review on
2026-09-26 (F20 in `guide/design.md`). `--dry-run` reports what the reset would do.
`tandem-review` resets the reviewer pane the same way, under the same rules, before every
review.

## Waiting

The launch prompt tells the builder to wait for a subagent in a single call, with the
longest timeout its wait tool accepts.

Without that instruction, waiting costs a lot. In one measured pairing session, 48 of the
builder's 173 API calls only waited for or listed agents. Together they used 4,544,623
input tokens and produced 2,285 output tokens, about 48 output tokens per call. The cause
was a 60-second wait that kept expiring. 18 of 46 waits timed out, and a child that took
three minutes cost three full-context calls that did nothing. One wait per child is the
minimum, so about 26 calls learned nothing.

The prompt names no tool and no number. One template drives `claude`, `codex`, `copilot`
and `opencode`, which name their wait settings differently, and a fixed value would break
as soon as a CLI renamed its parameter. Instead, the prompt states the rule and its cost,
which is what an agent needs to pick a sensible value itself.

The same rule covers long commands. In a session on 2026-09-25, the builder started the
project's 20-second check with a one-second wait, then checked back 21 times. That cost 3.0M
input tokens for 1.2k of output, because each check was a full-context call. The prompt now
tells the builder to run long commands in one blocking call, naming `tandem check` when the
project has a check. It also tells the builder to raise every timeout its tools have. In
Codex's code mode, a command runs inside a second, outer wait, and raising only one timeout
still polls (F19 in `guide/design.md`). The review prompt says the same about the one check
it runs.

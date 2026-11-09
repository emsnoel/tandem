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

A controller sends each task, runs the check and records the review. See
[The build engine](build-engine.md) and [Durable state](durable-state.md).

1. `tandem-handoff` starts a run (`tandem build start`). tandem splits the plan at its
   `Task N` headings. A plan with no task headings is one task.
2. The task prompt is a bounded packet: the task, the constraints and relevant context. It
   doesn't ask the builder to read the whole plan.
3. The builder acknowledges the prompt with `tandem-handback --started`, does the task and
   runs `tandem-handback --report FILE` with the identity from the prompt, adding `--stuck`
   if it couldn't finish. That handback goes through the mailbox to the relay bridge, never
   to the planner.
4. On its next tick the controller snapshots the tree, builds the patch, runs the check
   itself, outside the sandbox, and sends the review to the reviewer pane.
5. When the check passed and the reviewer accepts, the controller accepts the task and sends
   the next one in a fresh conversation. If the review asks for changes, it sends a repair.
6. tandem wakes the planner once, when the run finishes or pauses. The planner signs the run
   off on its stored evidence with `tandem-unpair`.

The builder never reviews, so a run needs a reviewer pane. `tandem-handoff` refuses without
one, and says how to pair one.

## What stops a build

The planner gets a note that says what stopped, why and what to run next. See
[When a run stops](build-engine.md#when-a-run-stops).

`task_input_budget` in `.tandem.json` sets each task's limit, and defaults to 3,000,000
input tokens. 0 turns it off. The most expensive task of that 8-task build, replayed in a
fresh conversation, came to 1.65M. So the default leaves room for a big task, and still stops
a missed reset or a runaway task long before a usage window runs out. tandem measures it
from the session logs of Codex, Claude and Copilot. When usage can't be read, a configured
budget pauses the run.

`tandem build status --json` shows each task's attempts, check and review.

We measured a live run on 2026-09-28, with codex-cli 0.158.0 and the eval fixture's 3-task
plan:

- Each task's first call used 22.5k to 23k tokens, the least a fresh conversation costs.
- No task read the plan or `AGENTS.md`.
- The bridge sent tasks 2 and 3 and finished without a person.
- The run passed `tandem-unpair`'s gate.

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

So just before it sends a task, the controller resets the builder. It
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
log for the task's usage, and to judge a turn that ended at once. So for any other prompt to a pane whose conversation Herdr doesn't name, `tandem agent prompt --as`
adds a last line to the prompt, `(tandem delivery <id>)`, and finds the turn's
conversation by that line.

**Codex 0.157 asks where the new conversation should run.** Its `/new` opens a native
picker ("Current checkout" or "New worktree") that Herdr can't see. The pane stays `idle`
and nothing changes. So when a reset command changes nothing for two seconds,
`tandem agent reset` presses the builder kind's confirm key once and waits again. For Codex
that is Enter, which picks the highlighted "Current checkout". If still nothing changes,
the reset is `unconfirmed`. tandem then sends nothing and names the pane. Typing into that screen lost a build prompt on 2026-09-28 (F21 in
`guide/design.md`).

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

tandem never sends a task into a conversation it could not reset. A Claude or Copilot
builder with no session id yet hasn't taken a turn since `tandem-pair`, so there is nothing to
reset.

tandem treats a builder that Herdr reports as `done`, a finished turn nobody has looked at
yet, as settled, the same as `idle`. Treating it as busy let a build carry its review on
2026-09-26 (F20 in `guide/design.md`). `--dry-run` reports what the reset would do.
`tandem-review` resets the reviewer pane the same way, under the same rules, before every
review.

## Waiting

The review prompt tells the reviewer to wait for a subagent in a single call, with the longest
timeout its wait tool accepts, and to run long commands in one blocking call. The task prompt
doesn't carry this rule yet.

Waiting without that rule costs a lot. In one measured pairing session, 48 of the builder's 173
API calls only waited for or listed agents. Together they used 4,544,623 input tokens and
produced 2,285 output tokens. In a session on 2026-09-25, the builder started the project's
20-second check with a one-second wait, then checked back 21 times, for 3.0M input tokens.

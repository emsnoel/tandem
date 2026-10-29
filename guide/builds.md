# Builds

## A fresh conversation for the build

Before the reviewer pane was required, reviews and the build happened in the same builder
pane, so without intervention the build inherited every review round: on 2026-09-25 it
began with 141k tokens of context against 26k for the first review, and its 46 calls cost
7.8M input tokens, three-quarters of the builder's total. Reviews now run in the reviewer
pane, but the builder's conversation can still hold an earlier build or a `tandem-ask`
exchange, and none of that history is needed.

So just before sending the launch prompt, `tandem-handoff` runs `tandem agent reset`,
which types the builder CLI's own new-conversation command into its pane — `/new` for
Codex, `/clear` for Claude Code — and waits for the pane to settle. The pane, its process
and `builder.json` are untouched, so the review still stands.

**A reset has to show itself; waiting is not proof.** Claude Code reports a new session id
the moment it resets. For Codex, what shows the reset is the new conversation starting, a
lifecycle change Herdr counts in `state_change_seq` for that pane alone. The reset then
reports `sent`. For Claude, after delivery `tandem-handoff` asks `tandem agent session --not
<old id>` for the id the build actually runs in. If it never moved, you get a warning that
the build may be carrying the review, and nothing is recorded.

**tandem never reads a Codex pane's conversation from Herdr.** Codex runs every
conversation on one shared app-server daemon, which the first Codex TUI on the machine
starts and every later one joins. Herdr's Codex hook runs inside that daemon and reads the
pane id from its environment, so it credits every Codex conversation to the pane that
started the daemon, and never gives another Codex pane one. A daemon started outside Herdr
credits none (measured 2026-10-12, codex-cli 0.162.1). So a Codex pane with no session
in Herdr may hold any number of turns: it is reset like any other, never skipped as empty.
A Codex task's conversation is the one whose log holds the task's nonce, for its usage and
for judging a turn that ended at once. A legacy prompt carries no nonce, so for any pane
whose conversation Herdr does not name, `tandem agent prompt --as` adds one last line
to the prompt, `(tandem delivery <id>)`, and finds the turn's conversation by that.

**Codex 0.157 asks where the new conversation should run.** Its `/new` opens a native
picker ("Current checkout" or "New worktree") that Herdr cannot see: the pane stays `idle`
and nothing changes. So when a reset command changes nothing for two seconds, `tandem
agent reset` presses the builder kind's confirm key once (Enter for Codex, which picks the
highlighted "Current checkout") and waits again. If still nothing changes, the reset is
`unconfirmed`, and `tandem-handoff` and `tandem-review` send nothing and exit 7, naming
the pane. Typing into that screen is what lost a build prompt on 2026-09-28 (F21 in
`guide/design.md`).

It resets **only a conversation that does not hold a build task still in progress.** That
id is recorded in `.tandem-relay/build-session`; a later handoff into the same session
leaves it alone, because the planner's `tandem-ask` follow-ups rely on it. When a task is
checked and done, `tandem build advance` clears that record: the ledger now carries what
later tasks need, so the next task's handoff resets as usual.

**A Copilot builder is reset with `/new`, confirmed from Copilot's own log.** Herdr keeps
reporting the `--session-id` the pane was launched with after `/new`, and its lifecycle
counter does not move, so Herdr cannot show the reset. Copilot writes `Registering
foreground session: <id>` to its process log, `~/.copilot/logs/process-<ms>-<pid>.log`, the
moment `/new` runs. `tandem agent reset` finds the pane's Copilot process with `herdr pane
process-info`, and counts the reset only when that log names a conversation opened after
the command. Nothing else counts: a log line in any other shape leaves the reset
`unconfirmed`, and the task is not sent. For the same reason tandem never takes a Copilot
pane's conversation id from Herdr: a task's conversation is the one its reset named, or the
one whose prompt holds the task's nonce in `~/.copilot/session-state`.

**An opencode builder is not reset.** Its `/new` does empty the conversation, but nothing
shows it until the next prompt: Herdr never reports an opencode session, its counter does
not move, and opencode writes no session, log line or state file before that prompt. A
reset nobody can confirm is not claimed (F18 in `guide/design.md`). A reset that is skipped never
blocks a build: an unsupported kind, or a builder that is still working or at a dialog
before the reset, prints one warning and delivers into the existing conversation. Only a
reset that was typed and could not be confirmed stops the handoff, as above. A Claude or
Copilot builder with no session id yet has not taken a turn since `tandem-pair`, so there is
nothing to reset and nothing to warn about. A builder Herdr reports as `done` (a finished turn nobody has
looked at yet) counts as settled, exactly like `idle`: treating it as busy is what let a
build inherit its review on 2026-09-26 (F20 in `guide/design.md`). `--dry-run` reports what
it would do. `tandem-review` resets the reviewer pane the same way, under the same rules,
before every review.

## Per-task builds

One conversation holding every task was the largest cost left once reviews and builds got
fresh conversations. On 2026-09-28 an 8-task build read the whole 2,229-line plan, the
conventions file (which Codex had already loaded) and three plan-execution skills before
task 1, 64k tokens of context before any edit. Every task's reads then rode along into the
next: task 1 began at 93k, task 5 at 208k. The build spent 15.5M input tokens and stopped
in task 5 when Codex's 5-hour usage limit ran out. Replayed with each task starting fresh,
the same work costs about 4.4M.

So tandem runs the plan one task per conversation. The steps below are the **legacy**
engine, which is still the default. A **durable** run splits the plan the same way, but a
controller sends each task, runs the check and records the review, and the planner signs the
run off on its stored evidence rather than a report. See
[guide/build-engines.md](build-engines.md) and [Durable state](durable-state.md).

1. `tandem-handoff` splits the plan at its `Task N` headings (`tandem build start`). Each
   brief in `.tandem-relay/build/task-N.md` is the plan's title, its opening section (goal,
   architecture, constraints, minus any `REQUIRED SUB-SKILL` line) and one task. A plan with
   no task headings is one task. No model tokens are spent writing briefs.
2. The launch prompt names the task, its brief and a ledger of the tasks before it. It does
   not ask for the whole plan, and asks for the conventions file only when the builder's
   CLI does not load it itself (Codex loads `AGENTS.md`, Claude Code loads `CLAUDE.md`).
3. The builder does the task, runs the check through `tandem check`, and runs
   `tandem-handback --task N --report FILE` (with `--stuck` if it could not finish). That
   always goes through the mailbox to the relay bridge, never to the planner.
4. The bridge runs `tandem build advance`: it waits for the builder's turn to end, records
   the files the task changed, what its conversation and any subagents cost, runs the check
   itself outside the sandbox, and decides. A passing task sends the next with
   `tandem-handoff --continue`, which resets the builder and re-runs the review gate, so a
   plan edited mid-build stops the loop instead of building half of each version.
5. The planner is woken once, when the build finishes or stops, with one report in
   `.tandem-relay/reports/` that `tandem-unpair --verified` signs off as before.

Both engines need a reviewer pane. A legacy `tandem build start` refuses with exit 23 when
`reviewer.json` records none, and the build records that pane's name. `tandem-handoff`
refuses a durable start in the same case, naming the same remedy. Every legacy task gets
a review round in the reviewer pane, and the launch prompt tells the builder not to review
the task or spawn a reviewer subagent. Only [review triage](jev.md#review-triage-optional-off-by-default),
when it is on, can skip a round. When it skips one, nothing reviews that task.

**What stops the loop**, each with the remedy in the planner's message:

| Stop | Why | To carry on |
| --- | --- | --- |
| `stuck` | the builder said it could not finish | `tandem-ask builder "..."`, then it hands back task N again; `tandem-handoff` to resend task N fresh; or, if the task is done, `tandem build accept --task N --because "why"` and then `tandem-handoff` |
| `red` | the check tandem ran failed | the same. Handing back with nothing changed reruns the same check and stops again |
| `budget` | the task's input tokens passed `task_input_budget`. A reviewer's `changes` on a task over its budget stops here too, and no fix is sent | find out why (`tandem build status`), then `tandem-handoff` continues with the next task |
| `handoff` | the next task could not be sent (a changed plan, a dialog) | fix it, then `tandem-handoff` |
| `unsettled` | the builder never finished its turn after handing back | check its pane, then `tandem-handoff` |
| `review_edited_tree` | the reviewer pane changed the task's files or added one while it reviewed, so its review does not count | read `git diff`; `tandem build accept --task N --because "why"`, or undo the edits and `tandem-handoff` |
| `review_rounds` | the reviewer still asked for changes after 3 rounds | read its findings; `tandem-ask builder "..."` to fix and hand back again, or `tandem build accept --task N --because "why"` |

`task_input_budget` in `.tandem.json` defaults to 3,000,000 input tokens per task, and 0
turns it off. The most expensive task of that 8-task build, replayed fresh, came to 1.65M,
so the default leaves room for a big task and still stops a missed reset or a runaway task
long before a usage window is gone. It is measured from Codex's own session logs; other
builder kinds report "not measured" and are never stopped by it.

`tandem build status` prints the ledger: each task's status, check result, files, calls,
input and peak context. `tandem check [-- ARGS]` runs the check with its output in a log
under `.tandem-relay/build/checks/`, and prints the exit code, the lines that name a failure
and the last few lines. Two raw Jest runs put about 80k characters into one build's context
on 2026-09-28.

Measured live on 2026-09-28 (codex-cli 0.158.0, the eval fixture's 3-task plan): each
task's first call was 22.5k to 23k, the fresh-conversation floor; no task read the plan or
`AGENTS.md`; the bridge sent tasks 2 and 3 and finished without a human; the report passed
`tandem-unpair`'s gate. On a plan that small one conversation never grows, so splitting it
costs more (about 1.2M against 1.0M in one conversation, subagents aside): each task pays
the ~22k entry fee on every call. The split pays off where tasks carry real reads and test
output forward, as in the 8-task build above.

## Waiting

The launch prompt tells the builder to wait for a subagent in a **single** call, using the
longest timeout its wait tool accepts.

That paragraph is there because the alternative is startlingly expensive. On one measured
pairing session, 48 of the builder's 173 API calls were orchestration — waiting and listing
agents. They consumed 4,544,623 input tokens and produced 2,285 output tokens between them,
roughly 48 tokens per call. The cause was a 60-second wait that expired: 18 of 46 waits
timed out, and a child taking three minutes forced three full-context round trips to sit
still. The floor is one wait per child, so about 26 calls were spent learning nothing.

The prompt names no tool and no number. One template drives `claude`, `codex`, `copilot` and
`opencode`, whose wait vocabularies differ, and a hardcoded value would rot the moment a CLI
renamed its parameter. It states the principle and the cost instead, which is what an agent
needs to pick a sensible value itself.

The same rule covers long commands. On a 2026-09-25 session the builder started the
project's ~20-second check with a one-second yield, then checked back 21 times — 3.0M input
tokens for 1.2k of output, each check-in a full-context call. The prompt now tells the
builder to run long commands, naming `tandem check` when the project has a check, in
one blocking call, and to raise *every* timeout its tooling has: under Codex's code mode a
command sits inside a second, outer yield, and raising only one still polls (F19 in
`guide/design.md`). The review prompt says the same about the one check it runs.

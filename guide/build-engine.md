# The build engine

Tandem runs a multi-task plan through one engine. It keeps progress in a SQLite database,
applies every change as one transaction, treats each prompt as a delivery that can be
uncertain, and ties every check and review to the exact code it saw. A run survives crashes
and restarts without losing or repeating work, and tandem refuses to accept work it has no
evidence for.

For commands and recovery steps see
[durable-runner-operations.md](durable-runner-operations.md). For the evidence behind the
design see [durable-runner-rollout-report.md](durable-runner-rollout-report.md).

## How a run goes

```
handoff -> build start --builder-kind K -> controller creates the run and the first attempt
bridge tick -> controller sends the task prompt (after a reset)
builder -> tandem-handback --started       (acknowledges the prompt by its nonce)
builder -> tandem-handback --report        (submits)
bridge tick -> controller snapshots the tree, builds the patch, runs the check,
               types the review prompt into the reviewer pane
bridge tick -> controller reads the settled pane's reply and records it
               (tandem build review-record does the same by hand)
bridge tick -> controller accepts the task and sends the next
               (or the final notice to the planner)
```

Progress lives in `state.sqlite` and an artifact store in the state home. The relay's
`active-run.json` marks the run.

## Ideas worth knowing

- **Attempt.** One try at a task. A repair is a new attempt linked to the old one. Events for a
  superseded attempt are quarantined with a reason, never applied.
- **Event and effect.** An event is a recorded fact (the builder started, a check finished).
  An effect is something the controller must do because of an event (type a prompt). Effects
  are claimed under a fenced lease so two controllers cannot both send.
- **Delivery unknown.** If tandem cannot tell whether a prompt reached the pane, the effect is
  `delivery_unknown`. It is never typed again. Only the builder returning that prompt's nonce
  settles it. A builder gets 90 seconds to do that. After that the run pauses with
  `writer_unknown`, and no new task starts while a writer is uncertain.
- **Nonce.** A random value in each prompt. A start acknowledgement carrying the wrong nonce
  is rejected.
- **Snapshot and patch.** The controller captures the tree before and after an attempt and
  stores the patch between them. The check and review are bound to that patch and tree. A
  tree that changed after the check cannot be accepted on the old result.
- **Fresh conversation.** Codex, Claude and Copilot have a verified reset command; Copilot's
  is confirmed from its process log. `tandem build start` refuses any other kind.
- **Pane review.** The review prompt goes to the reviewer pane. Each tick reads the settled
  pane, takes the last JSON object that names the open review job, and records it as `tandem
  build review-record` would. The result is bound to the job, patch and tree. With no valid
  reply after 10 minutes the run pauses as `review_unknown`.
- **Reviewer pane.** Required, because the builder never reviews. `tandem-pair` starts it on
  its own model from the `reviewer` block in `.tandem.json` or `--reviewer KIND`, and records
  it in `reviewer.json`. A start without one is refused with the remedy. Its passing review
  is the independent check, and a result from the builder's or planner's conversation pauses
  the run with `verification_identity`. See
  [architecture/review-handback.md](architecture/review-handback.md#a-reviewer-pane).
  Direct reviewer execution (a headless review process) exists in the code and is disabled
  for every kind.
- **Job.** One piece of work for an attempt: the builder's turn, a check, or a review. A
  check or review job completes when its result is recorded. A builder job completes when
  its attempt is accepted or replaced by a repair, and is cancelled when the run stops. A run
  with an open job cannot be archived.
- **Waiver.** `tandem build accept --task N --waive check|review|implementation --because
  "why"` lets a person overrule one requirement. It is recorded with a reason. `--task`
  also takes the id as status prints it (`task-3`). A review waiver clears a run paused as
  `review_unknown` on that review.
- **State home.** `$TANDEM_STATE_HOME`, else the user config directory plus `tandem/state`.
  One directory per checkout. Deleting the relay never deletes a run.

## Run states

`running`, `paused` (with a `pause_code`), `stopping`, `finished` and `archived`. Common pause
codes: `stopped`, `writer_unknown`, `builder_silent`, `builder_blocked`, `check_failed`,
`source_mutated`, `repairs_exhausted`, `plan_changed`, `context_too_large`, `memory_conflict`,
`scheduling_blocked`, `no_reviewer`.

## When a run stops

Tandem tells the planner once, with a note that says what stopped, why, and what to run next.
To print the note again, run `tandem-pause-note`. A failed check, for example, names the task,
the exit code and the end of the check's log.

The commands the notes point at:

| Intent | Command |
|---|---|
| See where the run is | `tandem build status --json` |
| Continue after a restart | `tandem build recover` |
| Stop | `tandem build stop`, then ticks until paused |
| Accept a task a person has checked | `tandem build accept --task N --waive ... --because ...` |
| Raise a budget | `tandem build budget --scope task\|run --limit N` |
| See usage | `tandem build usage --json` |
| Give up on a prompt no agent acknowledged | `tandem state settle-delivery` |
| Archive a settled run | `tandem state archive --run ID` |

## What the task prompt carries

A bounded packet: the task, the constraints and relevant context, up to `context_limit` bytes.
It also carries the project's settings and any ruling, which count toward that limit:

- `conventions_file`: the builder is asked to read it in full before it starts, unless the
  builder's CLI loads that file itself (Codex loads `AGENTS.md`, Claude Code `CLAUDE.md`).
- A `--because` ruling: the objection it overrules, then the ruling, which outranks the plan.
- `prompt_extra`: last, before the acknowledge and hand-back instructions.

A run keeps the settings it started with, so editing `.tandem.json` mid-run changes nothing
until the next run. The prompt doesn't carry staffing advice.

## Known limits

- Usage can be read for Codex, Claude and Copilot.
- A provider failure before the builder acknowledges its task is silent until the ten-minute
  acknowledgement timeout, unless the turn ends within 8 seconds with no model work: that send
  is recorded as not sent and retried, and after 3 the run pauses as `dispatch_failed`.
- A provider failure after the builder acknowledges its task pauses the run as
  `builder_silent` once the pane has been idle for 10 minutes. A failure that leaves the turn
  running, so the pane still shows it working, stays silent with no timeout.
- A builder held at an approval or question dialog pauses the run as `builder_blocked` once
  Herdr has reported its pane blocked for 10 minutes. Tandem never answers the dialog. Answer
  it in the pane, then `tandem state recover --run ID --root DIR` continues the run. A dialog
  Herdr reports as idle is timed as `builder_silent` instead.
- Herdr can report a pane as idle while an agent startup dialog is up. Start the agent to its
  prompt before starting a run.
- A stop waits for the builder's current turn.
- `tandem state gc --apply` cannot see a publisher that holds neither the writer lease nor the
  operation lock. Run it only when nothing is running.
- No command reconciles a run with a plan that was edited mid-run. The run pauses with
  `plan_changed`.

## Where the code is

| Area | Package or file |
|---|---|
| Controller (start, tick, recover, stop) | `internal/controller/` |
| Pane review reader, reviewer pane | `internal/controller/review_pane.go`, `reviewer_pane.go` |
| Prompt delivery under a fenced lease | `internal/runner/` |
| Bridge wake-ups (mailbox and Herdr events) | `internal/mailwatch/`, `internal/herdr/`, `tandem agent watch` |
| Storage, leases, GC, rebind, archive | `internal/state/` |
| Artifact store | `internal/artifact/` |
| Snapshots and patches | `internal/snapshot/` |
| Pure transition rules | `internal/workflow/` |
| Herdr transport and CLI | `cmd/tandem/build_transport.go`, `build_engine*.go`, `state*.go` |
| Shell entrypoints | `bin/tandem-bridge`, `tandem-handoff`, `tandem-handback`, `tandem-unpair` |

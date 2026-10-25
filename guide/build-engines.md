# Build engines: legacy and durable

Tandem has two ways to run a multi-task plan through a builder. This page says what each
one is, how they differ, how tandem picks between them, and what is and is not verified
about the newer one. For commands and recovery steps see
[durable-runner-operations.md](durable-runner-operations.md). For the evidence behind the
rollout decision see [durable-runner-rollout-report.md](durable-runner-rollout-report.md).

Status as of 2026-10-18: the **legacy** engine is the default. The **durable** engine is
opt-in, by environment variable or by `build_engine` in `.tandem.json`. Both ship in the
same binary.

## In one paragraph

The legacy engine keeps a build's progress in one JSON file and lets the relay bridge advance
it with shell and Go commands. The durable engine keeps progress in a SQLite database, applies
every change as one transaction, treats each prompt as a delivery that can be uncertain, and
ties every check and review to the exact code it saw. Durable exists to survive crashes and
restarts without losing or repeating work, to refuse to accept work it has no evidence for,
and to send less context to the builder.

## Why a second engine exists

The legacy engine worked for short, supervised builds. Its weak points showed on long ones.

| Problem in the legacy engine | What it caused |
|---|---|
| Progress is a file (`build.json`) edited by several commands | A crash or two writers at once could lose or corrupt the state |
| A prompt has no delivery record | After a restart nobody can tell whether the builder got a task, so it can be sent twice or never |
| A check or review result is not tied to a tree | A green result could describe code that had since changed |
| One long conversation can be resumed across tasks | Context and token use grow with every task |
| Token use is read per task and only from Codex logs | Hard to measure, hard to cap |

## At a glance

| | Legacy | Durable |
|---|---|---|
| Selected by | Default, `TANDEM_BUILD_ENGINE=legacy`, or `"build_engine": "legacy"` | `TANDEM_BUILD_ENGINE=durable`, or `"build_engine": "durable"` in `.tandem.json`, when the build starts |
| Where progress lives | `<repo>/.tandem-relay/build/build.json` | `state.sqlite` and an artifact store in the state home, outside the checkout |
| Marker | The `build.json` file | `<repo>/.tandem-relay/active-run.json` |
| How state changes | Commands rewrite the JSON file | One event per transaction, with a version check |
| Who drives it | The relay bridge calls `tandem build advance` after each handback | The relay bridge calls `tandem build tick` on every wake (a queued message, a state nudge, a status change of the builder or reviewer, or the 30-second backstop), and `recover` at startup |
| How the builder reports | `tandem-handback --task N --report FILE` | The same command, with the identity from the prompt (`--run --attempt --job --effect --nonce`), and `--started` first |
| Delivery of a prompt | Fire and forget | An effect with a fenced lease. An uncertain delivery is never retyped |
| Fresh conversation per task | Yes, by a reset command before each task | Yes, and a builder with no verified reset is refused unless `--share-context` |
| Who runs the check | `tandem build advance` after handback | The controller, which keeps the log and a certificate of what it ran against |
| Evidence of the code | Hashes of changed files | Full snapshots before and after, and the exact patch |
| Reviewer pane | Required. `tandem build start` refuses with exit 23 without one | Required. Start refuses without one, and a run started before the rule pauses as `no_reviewer` |
| Review | A review round in the reviewer pane for every task: `clean` accepts, `changes` sends a fix, at most 3 rounds. Review triage, when on, asks Jev first and may skip the round | A review prompt typed into the reviewer pane. Each tick reads the reply from that pane and records it; `build review-record` records one by hand |
| Accepting a task | The check passed and the reviewer returned `clean`, or triage skipped the review | A passed check and a passed or waived review, for this exact tree |
| Stop | Recorded in the file | Waits until the builder is seen idle before releasing the writer |
| Restart | Resumes from the file | `recover` reconciles uncertain deliveries and expired leases, then continues |
| Usage and budgets | Per-task input budget, Codex logs | Per-task and per-run limits, recorded per log line, unknown stays unknown |
| Plan with dependencies | Tasks in order | `Task-ID`, `Depends-On`, `Paths`, `Outputs` metadata, checked for cycles |
| Context sent per task | The brief and the shared conversation | A bounded packet: the task, the constraints, relevant decisions from earlier tasks. 24,000 bytes by default, checked for every task at start |
| Teardown sign-off | Every handback report signed off with `--verified` | Every task accepted with a passed check and a recorded review (or waivers) for its tree |
| History after teardown | Archived copy under `.tandem-history/` | Rows and artifacts kept in the state home. Export and archive commands |

## How tandem picks an engine

1. If `<relay>/active-run.json` exists, the engine is durable.
2. Otherwise, if `<relay>/build/build.json` exists, the engine is legacy. One exception: a
   durable start (by the variable or `build_engine`) over a **finished** legacy build archives that
   build to `.tandem-history/legacy/`, verifies the copy, removes it from the relay and
   starts a durable run. A new legacy start already does the same with a build it replaces.
   Over an **unfinished** legacy build the durable start is refused, naming the build and
   `tandem state import-legacy --active`. A build you already archived by hand with
   `tandem build archive` is copied once more, since the relay still holds it.
3. If both exist, tandem refuses and asks you to look. It does not guess.
4. If neither exists, a new build uses `TANDEM_BUILD_ENGINE` when it starts. If that is unset
   or empty, it uses `build_engine` from `.tandem.json`. With neither, the default is legacy.
   `legacy` means legacy and `durable` means durable; any other value is refused.
5. After a build has started, neither setting is read again. A run keeps the
   engine it started with, so changing the variable mid-run does nothing.

A marker whose engine or protocol version this tandem does not speak is refused, with exit
code 23, before anything is read or written. `tandem state engine` reports the result.

## The legacy engine

```
handoff -> build start -> send task 1 -> builder works
       -> builder runs tandem-handback --report
       -> bridge runs tandem build advance
            waits for the builder to settle, measures it, runs the check,
            asks Jev, sends a review round to the reviewer pane or the next task
```

State is `build.json`: a list of tasks with a status each (todo, sent, reviewing, done, red,
stuck), the current task, and a stop reason. Files and tokens are measured by `advance`.
It still handles every build that started before the durable engine existed, and it is the
fallback if the durable engine misbehaves.

## The durable engine

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

### Ideas worth knowing

- **Attempt.** One try at a task. A repair is a new attempt linked to the old one. Events for a
  superseded attempt are quarantined with a reason, never applied.
- **Event and effect.** An event is a recorded fact (the builder started, a check finished).
  An effect is something the controller must do because of an event (type a prompt). Effects
  are claimed under a fenced lease so two controllers cannot both send.
- **Delivery unknown.** If tandem cannot tell whether a prompt reached the pane, the effect is
  `delivery_unknown`. It is never typed again. Only the builder returning that prompt's nonce
  settles it. The run pauses with `writer_unknown` and no new task starts while a writer is
  uncertain.
- **Nonce.** A random value in each prompt. A start acknowledgement carrying the wrong nonce
  is rejected.
- **Snapshot and patch.** The controller captures the tree before and after an attempt and
  stores the patch between them. The check and review are bound to that patch and tree. A
  tree that changed after the check cannot be accepted on the old result.
- **Fresh conversation.** Codex, Claude and Copilot have a verified reset command; Copilot's
  is confirmed from its process log. OpenCode has none that can be confirmed, so a run must
  say `--share-context` to send it a task in the conversation it has.
- **Pane review.** The review prompt goes to the reviewer pane. Each tick reads the settled
  pane, takes the last JSON object that names the open review job, and records it as `tandem
  build review-record` would. The result is bound to the job, patch and tree. With no valid
  reply after 10 minutes the run pauses as `review_unknown`.
- **Reviewer pane.** Required on both engines, because the builder never reviews.
  `tandem-pair` starts it on its own model from the `reviewer` block in `.tandem.json` or
  `--reviewer KIND`, and records it in `reviewer.json`. A durable start without one is
  refused with the remedy, and a run started before this rule pauses as `no_reviewer`. Its
  passing review is the independent check, and a result from the builder's or planner's
  conversation pauses the run with `verification_identity`. See
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

### Run states

`running`, `paused` (with a `pause_code`), `stopping`, `finished` and `archived`. Common pause
codes: `stopped`, `writer_unknown`, `check_failed`, `source_mutated`, `repairs_exhausted`,
`plan_changed`, `context_too_large`, `memory_conflict`, `scheduling_blocked`, `no_reviewer`.

## Command map

| Intent | Legacy | Durable |
|---|---|---|
| Start | `tandem build start --plan F --root R`, with a reviewer recorded in `reviewer.json` | the same, plus `--builder-kind K`, with `TANDEM_BUILD_ENGINE=durable`. `--reviewer NAME --reviewer-kind K` override the recorded reviewer |
| Mark a task sent | `tandem build sent` | not used. The controller sends |
| Handback processing | `tandem build advance --task N` | `tandem build tick` (called by the bridge) |
| Review round | `tandem build review-sent`, Jev | `tandem build review-record --result FILE` |
| Status | `tandem build status` | `tandem build status --json` |
| Stop | `tandem build stop` | `tandem build stop`, then ticks until paused |
| Accept a stuck or red task | `tandem build accept --task N --because ...` | `tandem build accept --task N --waive ... --because ...` |
| Continue after a restart | `tandem build start` resumes from the file | `tandem build recover` |
| Budget | set at start | `tandem build budget --scope task\|run --limit N` |
| Usage | `tandem usage ROLLOUT...` | `tandem build usage --json` |
| Archive | `tandem build archive` | `tandem state archive --run ID` |

Commands that belong to one engine refuse to run against the other. A legacy command on a
durable run says which durable command to use instead.

## Moving between engines

- **Legacy build to durable:** a finished legacy build can be kept as history with
  `tandem state import-legacy --archive`. A stopped, unfinished build can be carried into a
  durable run with `tandem state import-legacy --active DIR --quiescent`. That checks that
  nothing is writing, archives the build, starts a run over the whole plan, and carries the
  tasks the legacy engine finished as accepted without evidence. Their check and review stay
  pending, and the first unfinished task starts as a fresh attempt.
- **Durable back to legacy:** there is no conversion. Stop the run, let it settle, export it,
  archive it, and start a separate legacy build if you need one. A durable run is never
  reinterpreted as a legacy one.

## What is verified and what is not

Verified by automated tests: the state machine, crash and restart cases, delivery and nonce
rules, snapshots, checks, review recording, usage, budgets, migration, export, rebind, GC and
teardown, and a four-builder matrix (legacy and durable) against a fake Herdr.

Verified live, one run per CLI on a two-task plan: launch, fresh task, acknowledgement, submit
and check, pane review, accept, stop and restart for Codex, Claude, Copilot and OpenCode. Codex
usage was confirmed against its logs over a full plan.

Not verified: any cost or latency saving, a repair attempt live, a controller crash during a
send live, a full plan on Claude, Copilot or OpenCode, direct reviewer execution, and a
legacy review round in a live reviewer pane (it is tested against a fake Herdr only).

Known limits of the durable engine:

- Usage can be read for Codex, Claude and Copilot. A configured budget pauses for OpenCode.
- A provider failure in the builder is silent until the ten-minute acknowledgement timeout,
  unless the turn ends within 8 seconds with no model work: that send is recorded as not
  sent and retried, and after 3 the run pauses as `dispatch_failed`.
- Herdr can report a pane as idle while an agent startup dialog is up. Start the agent to its
  prompt before starting a run.
- A stop waits for the builder's current turn.
- `tandem state gc --apply` cannot see a publisher that holds neither the writer lease nor the
  operation lock. Run it only when nothing is running.
- No command reconciles a run with a plan that was edited mid-run. The run pauses with
  `plan_changed`.

## Which to use

Use the legacy engine for short, supervised builds, for a build that is already in flight, and
as the fallback if a durable run behaves in a way the report does not cover. Use the durable
engine for long builds, for unattended runs, and when you need recovery after a crash, evidence
tied to the exact tree, or a record of what each task cost. Changing the default is a separate
decision with its own gates, listed in the rollout report. Triage skipping is a separate switch
(`HERDR_TRIAGE_SKIP=1`) and stays off in both engines.

## Where the code is

| Area | Package or file |
|---|---|
| Engine selection | `internal/controller/engine.go` |
| Controller (start, tick, recover, stop) | `internal/controller/` |
| Pane review reader, reviewer pane | `internal/controller/review_pane.go`, `reviewer_pane.go` |
| Prompt delivery under a fenced lease | `internal/runner/` |
| Bridge wake-ups (mailbox and Herdr events) | `internal/mailwatch/`, `internal/herdr/`, `tandem agent watch` |
| Storage, leases, GC, rebind, archive | `internal/state/` |
| Artifact store | `internal/artifact/` |
| Snapshots and patches | `internal/snapshot/` |
| Pure transition rules | `internal/workflow/` |
| Herdr transport and CLI | `cmd/tandem/build_transport.go`, `build_engine*.go`, `state*.go` |
| Legacy engine | `internal/build/`, `cmd/tandem/build*.go` (advance, review, accept) |
| Shell entrypoints | `bin/tandem-bridge`, `tandem-handoff`, `tandem-handback`, `tandem-unpair` |

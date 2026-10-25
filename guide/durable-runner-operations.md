# Durable runner operations

The durable engine is opt-in. Nothing here changes the default: a build starts on the
legacy engine unless `TANDEM_BUILD_ENGINE=durable` is set at the start, or
`"build_engine": "durable"` is in `.tandem.json`, and a run keeps the engine it started with. This guide covers what an operator needs to inspect, move or
undo a durable run. Commands marked "sample" are checked against the CLI's usage text by
`TestOperationsCommandsMatchUsage`.

## Choosing an engine

A new build is durable only when `TANDEM_BUILD_ENGINE=durable` is set for `tandem build
start` (or `tandem-handoff`, which passes it through), or `"build_engine": "durable"` is in
`.tandem.json` and the variable is unset. The variable wins when both are set. Every later command follows the
relay: an `active-run.json` marker means durable, a `build/build.json` means legacy, both
means refused, and the environment variable is not read again. The one exception is a
finished legacy build: a durable start archives it (as a new legacy start would), removes it
from the relay and starts the run. Its output names the archive as `archived_legacy`. An
unfinished legacy build refuses a durable start with what to do instead. Triage skipping is a
separate switch (`HERDR_TRIAGE_SKIP=1`) and stays off until its own calibration report
passes. Neither default is changed by this release.

The builder kinds are Codex, Claude, Copilot and OpenCode. Codex, Claude and Copilot have a
verified way to start a fresh conversation; Copilot's is confirmed from its own process log,
because Herdr cannot see it. OpenCode has none that can be confirmed, so a durable run sends
an OpenCode builder a task only when started with `--share-context`. `tandem-handoff` cannot
pass that flag, so an OpenCode durable build starts with `tandem build start`.

## Where things live

| What | Where |
|---|---|
| State home | `$TANDEM_STATE_HOME`, else `<user config dir>/tandem/state` (macOS: `~/Library/Application Support/tandem/state`) |
| Checkout registry | `<state home>/registry.json`, locked by `registry.lock` |
| One checkout's state | `<state home>/<checkout id>/state.sqlite` |
| Artifacts | `<state home>/<checkout id>/artifacts/sha256/<first two hex>/<hash>` |
| Locks | `<state home>/<checkout id>/locks/` (`operation.lock`, one `tick-<run>.lock` per run) |
| Schema-upgrade backups | `<state home>/<checkout id>/backups/state-v<N>-<random>.sqlite` |
| Relay (transport only) | `<repo>/.tandem-relay/` |
| Durable run marker | `<repo>/.tandem-relay/active-run.json` |
| Marker of an archived run | `<repo>/.tandem-relay/active-run.<run id>.archived.json` |
| Run mailbox | `<repo>/.tandem-relay/durable/<run id>/` (`manifest.json`, `q/`, `payloads/`, `quarantine/`) |
| Legacy build record | `<repo>/.tandem-relay/build/build.json` |
| Archived legacy builds | `<repo>/.tandem-history/legacy/<id>-<random>/` |

The relay is transport. Deleting it never deletes a run: the rows and artifacts are in the
state home, which is deliberately outside the checkout and outside any relay.

## Looking at a run

Samples:

    tandem state status --json --root DIR
    tandem build status --root DIR --json
    tandem build usage --root DIR --json
    tandem task context --attempt ID --explain --json --root DIR

`state status` says whether a store exists, its schema, the current run and why it is
paused. `build status` lists tasks and attempts with their implementation, check and
review states. `build usage` prints null for what is unknown. Unknown usage is never zero.

## An unknown delivery

A prompt that was typed but not confirmed is `delivery_unknown`. Tandem never types it
again: only the builder returning the prompt's nonce proves it was read. The run pauses
with `writer_unknown`, and no new task starts while a writer is uncertain.

1. Look at the pane. If the builder is working on the task, it read the prompt.
2. Export the run and read the manifest. Every unresolved delivery is listed under
   `unresolved`, with its effect id.

       tandem state export --run ID --output PATH --root DIR

3. If the builder read it, it can acknowledge with the identity from its prompt
   (`tandem-handback --task N --run ID --attempt ID --job ID --effect ID --nonce ID --started`).

   Durable task acknowledgements and handbacks use the active-run marker and the
   identity from the task prompt. Run the commands as printed even if your agent
   shell does not inherit `HERDR_ENV` or `HERDR_ROLE`. Keep every identity flag.
   Legacy handbacks still require `HERDR_ENV=1`.

   Then reconcile:

       tandem state recover --run ID --root DIR

4. If it did not, stop the run (`tandem build stop --root DIR`), let it settle, and
   decide whether to start again. A run with an unresolved delivery cannot be archived or
   rebound until it is settled.

`state recover` also ends any builder job whose attempt is already settled. A run that an
older tandem drove can hold such jobs, and they block its archive (see Rollback and
downgrade below).

`state recover` prints the run's state. A pause that needs you is a successful result
with `state` paused and a `pause_code`, not an error.

## A run needs a reviewer pane

Every review goes to the reviewer pane, and the builder never reviews. `tandem build start`
reads the pane `tandem-pair` recorded in `reviewer.json`, as the legacy start and an import
do; `--reviewer NAME --reviewer-kind KIND` override it. With no record, or one that is
incomplete or names the builder or the planner, it refuses with exit 23 and creates nothing.
The refusal names the remedy: declare a `reviewer` block in `.tandem.json` and re-run
`tandem-pair`, or run `tandem-pair --reviewer KIND`.

A run started before this rule has no reviewer pane in its stored configuration. On its
next tick while running it pauses as `no_reviewer`, and `tandem build status` gives the way
on as the pause's reason. Stop it, let it settle, archive it, re-pair with a reviewer, and
hand off again. `state recover` ends any builder job left open, which would block the archive:

    tandem build stop --root DIR
    tandem state recover --run ID --root DIR
    tandem state archive --run ID --root DIR

## A review not recorded in time

A review prompt waits 10 minutes for its answer. Each tick reads the end of the reviewer's
pane once the pane has settled, takes the last JSON object in it that names the open review
job, and records it as `tandem build review-record` would: same validator, same job, patch
and tree binding. A reply wrapped by the CLI's own screen (Codex indents the rest of a long
line) is joined first. The example object in the review prompt carries the job's real ids,
but its status lists the choices (`passed|changes_requested|failed`), so it is never taken for
the reply. Nothing needs to be done for a reply that is there.

If no valid reply is found by then (the reviewer answered in prose, named another job, or
reviewed a different tree), the prompt becomes `delivery_unknown` and the run pauses as
`review_unknown`. `tandem build status` gives this as the pause's reason. Nothing is lost:

- Record the result. However late it comes, it answers the prompt, clears the pause, and
  the next tick accepts the task if the review passed.

      tandem build review-record --result FILE --root DIR

- Or overrule the review. A review waiver makes the prompt moot and clears the pause too.

      tandem build accept --task task-3 --waive review --because "why" --root DIR

`--task` takes the id as `tandem build status` prints it, or the number alone.

## Raising a budget

Limits count total input tokens and are stored in the run. Zero is off. A task limit
applies to every task, and a run limit to the whole run.

    tandem build budget --scope task --limit 4000000 --root DIR
    tandem build budget --scope run --limit 20000000 --root DIR

Raising a limit resumes only a run that was paused for scheduling. Unknown usage under a
limit pauses by design. Usage can be read for Codex, Claude and Copilot builders; for an
OpenCode builder a configured limit pauses, so leave limits off there.

## Collaborations (opt-in)

A collaboration is the identity above runs that a whole piece of work is accounted to:
planning, every run and replacement run, reviews and closeout. Nothing uses one unless a
run is started with `--collaboration`; without it a run behaves as before.

    tandem collaboration create --policy policy.json --root DIR
    tandem collaboration register-source --collaboration ID --source planner.json --root DIR
    tandem build start --plan PATH --root DIR --builder-kind codex --reviewer reviewer --reviewer-kind claude --collaboration ID

`create` prints the durable id. The policy is a JSON object whose optional
`input_ceilings` and `output_ceilings` map a provider to a token ceiling. `register-source`
records a source that is already running, such as the planner's session, with the message
its ownership starts after (`start_boundary`). One planner session belongs to one
collaboration at a time. A run in a collaboration registers each builder and reviewer it
launches before the prompt is sent, and binds the session the launch receipt reports. A
receipt with no session leaves the source unresolved, so its usage is reported as
incomplete rather than guessed. A collaboration that is closing admits no new launch.

With `"enforce_budgets": true` in the policy, every launch is also admitted before it is
sent. Admission checks the collaboration's stop generation, the run and its writer lease,
and that the provider's `input_reservations` and `output_reservations` fit under its
ceilings with what is already spent and reserved. A provider with no ceilings is refused
unless the policy lists it in `unlimited_providers`. A launch that is not admitted is not
lost: the run pauses as `scheduling_blocked` with the launch pending, and its send retries
are untouched. A person stops, resumes or raises a budget with a recorded reason:

    tandem collaboration stop --collaboration ID --actor you --reason hold --root DIR
    tandem collaboration amend --collaboration ID --provider codex --input-ceiling 4000000 --actor you --reason room --root DIR
    tandem collaboration resume --collaboration ID --actor you --reason continue --root DIR

Usage is collected whether or not budgets are enforced, after every tick and stop and on
archive, reading only what each log gained since the last read:

    tandem collaboration collect --collaboration ID --root DIR
    tandem collaboration close --collaboration ID --root DIR
    tandem collaboration report --collaboration ID --json --root DIR

`collect` stores a new snapshot revision only when something changed, and the revision it
replaces is marked superseded. `close` first freezes the collaboration, so no launch, paid
closeout included, starts after it, then collects. The collaboration becomes closed only
when every source is final: its work has ended and its log ends in terminal evidence. A
quiet log is not terminal evidence. A planner's session need not exit; its owned segment
closes at a named message. Otherwise it stays closing and the snapshot lists what is
missing. A run archive collects too, but leaves the snapshot provisional, because a child
or a closeout may still be running; a child that finishes later keeps the collaboration it
was launched for. A log that is missing, cut back or still being written gives a lower
bound, never zero, and a total that is unknown says unknown. `report` reads the stored
snapshot and no log. None of these commands calls a model.

`resume` and `amend` also resume the checkout's run if a held launch paused it. A launch
admitted before a stop may still be sent; the stop cannot recall it. Stopping, collecting
usage and archiving stay available at any ceiling.

## A task too large for the context limit

Every task's prompt has a required part: tandem's header and footer, the plan's opening
section and the task's brief. That part is never cut. Optional context (decisions from
earlier tasks) fills what is left of the limit. The limit is 24,000 bytes unless the run
was started with another. `tandem build start` measures every task's first prompt before it
creates anything. If one does not fit, it exits 23, names each task with the bytes it
needs, and nothing is created. Raise the limit for the start:

    tandem build start --plan PATH --root DIR --builder-kind codex --reviewer reviewer --reviewer-kind claude --context-limit 32000

`tandem-handoff` takes the same `--context-limit N`, or reads `context_limit` from
`.tandem.json`. A repair attempt adds the review's findings, and a later task can need a
required decision, so a run can still pause with `context_too_large` after it started. That
run keeps its limit: stop it, archive it and start a new run with a higher one.

## Export, import and rebind

    tandem state export --run ID --output PATH --root DIR
    tandem state import-legacy --archive PATH --root DIR
    tandem state import-legacy --active DIR --quiescent
    tandem state rebind --checkout ID --root PATH

Export writes a new directory (never over an existing one) with `manifest.json`, every
artifact the run names, and `COMPLETE` last. A directory without `COMPLETE` is a failed
export. `--archive` keeps a finished legacy build as history. `--active` moves a stopped
legacy build onto the durable engine: tasks the legacy engine finished are carried
without evidence and their check and review stay pending. Rebind points a checkout at a
moved worktree and needs the checkout idle.

## Retention

    tandem state gc --root DIR
    tandem state gc --apply --root DIR

The first form only reports. `--apply` needs an idle checkout and refuses while anything
is publishing. It deletes only artifacts that no row, reference or snapshot names, never a
run, and never by age. Missing or corrupt referenced artifacts are listed and left alone.
Stop every run first.

## Rollback and downgrade

Rolling back from the durable engine is pause, settle, export, keep the store:

    tandem build stop --root DIR
    tandem state recover --run ID --root DIR
    tandem state export --run ID --output PATH --root DIR
    tandem state archive --run ID --root DIR

`archive` refuses while a job or a delivery is open. A builder job ends when its attempt is
settled: `completed` when the attempt is accepted or replaced by a repair, and `cancelled`
when the run stops. A run finished or stopped by an older tandem kept its builder jobs
`running` forever; `tandem state recover --run ID` ends them, and the archive then goes
through. `recover` never resumes a stopped run and does not take its writer lease back.
Archiving sets the marker aside as `active-run.<run id>.archived.json`, so the next build
in the checkout starts clean.

Then start a separate legacy run if you need one. A durable run is never turned back into
a legacy one. An older tandem refuses a relay it does not speak (`tandem state engine`
exits 23 with the remedy) and refuses a store with a newer schema. Do not delete the state
home to "undo" a run: it holds the history. To go back to a pre-upgrade schema, copy a
backup from `backups/` over `state.sqlite` with nothing running.

Schema 2 adds the collaboration ledger and schema 3 the review handback tables. Rolling
either back means starting runs without the opt-in flags (`--collaboration`, and later
`--review-delivery mailbox`) on the same binary; the tables stay and nothing reads them.
An older tandem refuses a store with a newer schema.

### Direct review handback (opt-in)

A collaboration run can also start with `--review-delivery mailbox` and an
`--acceptance-policy` file. Reviewers then hand their results to Tandem with
`tandem-handback --review-started` and `--review-result FILE`. The reviewer pane's passing
review is the independent check, so the planner is asked to verify only an attempt with no
reviewer result, as after a waived review. A run keeps the protocol it started with. See
`guide/architecture/review-handback.md` for the flow, the manual stop and review-record
paths, and the planner-turn counters.

### Bounded recovery (opt-in)

A collaboration run can also start with `--recovery-policy`. A failed declared check or a
review that asks for a concrete change is then repaired automatically once per piece of
work, across replacement runs, and any other failure becomes one decision request for the
planner. `tandem build reconcile` hands back a fix made by hand. See
`guide/architecture/bounded-recovery.md`.

## Teardown

`tandem-unpair` on a durable run first signs it off on the run's own evidence: every task
accepted with a passed check and a recorded review for its final tree, or a waiver of either.
It does not read handback reports, which a durable run never writes, and it lists any an
earlier legacy build left as not part of the run. A task that lacks evidence is named with
what it lacks (exit 5), and a run with nothing accepted exits 4. The check command still runs.
Then it stops the run, waits for it to settle, archives it and removes the pane, the bridge
and the relay. If the run does not settle, or an open job or
delivery blocks the archive, nothing is closed or deleted. `--force` closes anyway and
keeps the state.

## Live smoke procedure

Last run on 2026-10-03, one two-task plan per kind, against Herdr 0.8.2. The results and
what has changed since are in [durable-runner-rollout-report.md](durable-runner-rollout-report.md).
The fixtures prove compatibility against a fake Herdr. They do not prove any agent CLI
works. A live pair is authorised by a person and costs real tokens. For each kind, record
the OS, the Herdr version, the agent CLI version, the model and settings, and the
transport. Then observe:

1. launch with the same model, effort and autonomy flags as before;
2. a fresh task (a reset, or a new session, seen before the prompt);
3. the acknowledgement carrying the attempt's nonce;
4. a submit that reaches the queue, and the controller running the check;
5. a pane review whose result is read from a settled pane;
6. a stop that is seen to end the builder's turn before the writer is released;
7. a restart (`tandem state recover`) that resends nothing.

Reading a pane review by hand: each tick already reads the reply itself (see
[A review not recorded in time](#a-review-not-recorded-in-time)). To record one yourself,
remember the reply is one JSON object and can be longer than the terminal is wide, so read
it unwrapped (`herdr agent read NAME --source recent-unwrapped --lines 120`) before saving
it for `tandem build review-record`. Copilot truncates it otherwise. Some CLIs
print the object over several lines (a Codex reviewer did on 2026-10-11), so save the whole
object, from its opening brace to its closing one, not only the line naming `job_id`.

If a builder's model fails, the send says so. A task or review prompt whose turn ends within
8 seconds with no new tokens in the conversation's log did no model work: a model the CLI
rejects, an expired login and used-up quota all look like this. The send is recorded as not
sent, with the pane's last lines kept as the reason, and retried with backoff. After 3 such
sends the run pauses as `dispatch_failed`, and `tandem build status` shows the reason as
`pause_reason`. Fix what the pane shows, then restart the run. A second `tandem-handoff`
is refused while the run is active, so stop it, end its open jobs and archive it first:

    tandem build stop --root DIR
    tandem state recover --run ID --root DIR
    tandem state archive --run ID --root DIR

Then re-pair with a reviewer if the pair has none, and hand off again.

A builder's usage is read from the log of the conversation it ran in. Herdr names a pane's
conversation only for Claude. Its Codex hook runs in Codex's shared app-server daemon and
credits every Codex conversation to the pane that started the daemon, so a Codex
pane's id is never taken from Herdr. When the builder hands back, the run searches the
Codex logs written since the run started for the task's nonce, which its prompt carried. Two conversations holding one nonce are not told apart; the
usage is then reported as unavailable, and a run with an input limit pauses as
`scheduling_blocked` rather than guess.

A Copilot builder's conversation never comes from Herdr, which reports the id the pane was
launched with even after `/new`. The task's receipt names the conversation the reset found
in Copilot's process log, and the usage is the `session.usage_record` events in
`~/.copilot/session-state/<id>/events.jsonl` (`$COPILOT_HOME` moves it). When the receipt
names none, the run searches those logs for the task's nonce, as for Codex. A collaboration
does not count Copilot sources yet: the collaboration ledger has no Copilot parser.

A pass for Codex says nothing about Claude, Copilot or OpenCode. Anything not observed is
recorded as unverified.

## Rollout gates

The default engine changes only in a separate, reviewed release patch after all of these
hold: the correctness gates, all four builders verified live, the recovery gates, and the
measured cost targets (a provisional 25% median uncached-input improvement and 30%
total-input improvement on long builds, small builds no worse than 10%, p95 latency no
worse than 20%, at least ten paired runs per main scenario). If quality is worse, the
default does not change however cheap the engine is. Triage skipping has its own release
decision and stays off until then.

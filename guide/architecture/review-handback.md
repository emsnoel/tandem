# Direct review handback

Status: `--review-delivery mailbox` and `--acceptance-policy` are opt-in. A run started without them behaves as it did before. The reviewer pane is not opt-in: see "A reviewer pane" below.

## What it changes

In the default (manual) delivery, a reviewer answers in its pane. Each tick reads the settled pane, takes the last JSON object that names the open review job, and records it as `tandem build review-record` would; a person can still run `review-record` by hand. With direct handback, the reviewer sends its result straight to Tandem's mailbox with `tandem-handback`. Tandem checks it, applies it once the reviewer's turn has ended, and moves the run on. The reviewer pane's passing review is the independent check. The planner is asked to verify only an attempt with no reviewer result, as after a waived review, and is told once when the run finishes.

No model turn is spent to poll, forward or approve an advance.

## Starting a run

```text
tandem build start --plan PATH --root DIR --builder-kind KIND \
  --reviewer reviewer --reviewer-kind KIND \
  --collaboration ID --review-delivery mailbox --acceptance-policy policy.json
```

- `--review-delivery manual` is the default. It takes no policy.
- `--review-delivery mailbox` needs `--collaboration` (the reviews are accounted there) and `--acceptance-policy`.
- Any other value, a policy without `mailbox`, or a builder kind Tandem has no transport for is refused before anything is created. A refused start leaves no run, no marker and no state.

The acceptance policy has two mandatory stages, `builder_review` and `independent_verification`. The `builder_review` stage keeps its stored name, but its prompt goes to the reviewer pane. It can choose who reviews and with which model. It cannot make acceptance easier: a missing stage, a self-review for the independent stage, or a skipped check is refused at start.

## What is stored, and what is not read again

At start Tandem hashes the run's configuration, including the policy and the review protocol, and stores it. Every later step reads that stored copy.

- A run started before this change stays on protocol 2. Setting `TANDEM_BUILD_ENGINE` or any other variable later does not move it.
- The helper that writes mailbox messages cannot reach the state, so the run's manifest carries a copy of the protocol. `tandem build event new` writes version 3 only when the manifest says the run opted in. `tandem build event ingest` reads the stored configuration, not the manifest, and refuses a version 3 message for a run that did not opt in.
- After a restart, archived mailbox traffic is replayed idempotently. Nothing is dispatched or prompted twice.

## What the reviewer does

Each mailbox review effect creates a private directory under `<relay>/durable/<run>/review/<effect>/`. It contains an executable `handback` and an initially empty `verdict.json`. The prompt gives the paths and asks the reviewer to:

1. Run `<path>/handback --started` when it begins.
2. Write one verdict to `<path>/verdict.json`.
3. Run `<path>/handback`, then stop when it succeeds.

The verdict contains only the reviewer's judgment:

```json
{"schema_version":1,"status":"passed","findings":[]}
```

`changes_requested` needs at least one finding. Each finding has `severity`, `path`, `disposition` and `message`, with optional 1-based `start_line` and `end_line`. The existing severity, disposition and blocking-finding rules apply unchanged.

The generated command holds the run, task, attempt, job, effect and nonce, and delegates to `tandem-handback`. The reviewer copies no identifiers or hashes. The command changes to the repository root, so it also works when invoked from another directory. Retrying prompt creation preserves a verdict already written.

Before writing any queue files, the CLI checks the verdict using the workflow validator. A malformed verdict exits nonzero with its exact reason, so the reviewer can repair it in the same turn. These checks do not open the state database.

Tandem stages valid submissions and applies them only after the reviewer has finished its turn. It binds the verdict to the stored job's identifiers and hashes, then runs the existing validation, current-attempt, reviewer-identity, tree and transaction-fence checks. Removing echoed hashes transfers responsibility for the tree binding to the controller: it checks the checkout against `job.TreeHash` again before recording the bound result, and pauses with `source_mutated` if it moved. The exact submitted verdict remains an immutable artifact, separate from the bound result.

Complete old-style result objects remain accepted, but any supplied identity field requires the full identity and matching hashes. Tandem never repairs a supplied identity. Manual `review-record` and protocol 2 continue to require the full result.

A rejected submission records its reason on the attempt and in `review/<effect>/rejected.txt`. `build status --json` exposes `review_rejection` on the current attempt, and a `review_unknown` pause includes the reason. Once its pane settles, the reviewer receives one correction prompt per rejected payload. Repeated ticks do not repeat that prompt, and a successful result clears the current rejection. Ambiguous feedback delivery is not retried automatically.

### Configured handoffs

`tandem-handoff` forwards these `.tandem.json` settings when starting a new durable run:

```json
{
  "build_engine": "durable",
  "review_delivery": "mailbox",
  "acceptance_policy": "review-policy.json",
  "collaboration": "COLLABORATION_ID"
}
```

Relative policy paths resolve against the repository root. The collaboration must already exist, and the policy must satisfy both mandatory stages. An existing run keeps its recorded protocol and policy; changing the manifest does not migrate it. Protocol 2 pane scanning remains for those persisted runs and for explicit manual delivery. Removing it requires a separate migration of those runs.

## Stopping, recording by hand and closing

Everything the mailbox does can still be done by a person.

- `tandem build stop` stops the run. A result that arrives later is staged and applied to nothing.
- `tandem build review-record` records a review by hand and goes through the same gates: the same validator, the same patch and tree binding, and for the independent stage the same rule that the verifier is not the writer. A manual record cannot show which conversation it came from, so only the configured sessions can show that the verifier is distinct.
- `tandem collaboration stop`, `resume` and `amend` control admission and budgets. `tandem collaboration close` finishes the accounting.

## A reviewer pane

Status: required. The builder never reviews, on either engine. A durable start without a reviewer pane is refused before anything is created, naming the remedy: declare a `reviewer` block in `.tandem.json` and re-run `tandem-pair`, or run `tandem-pair --reviewer KIND`. A run started before this rule, whose stored configuration has no reviewer pane, pauses with `no_reviewer` on its next tick while it is running. A run already paused for another reason gets there once it resumes. Its pause reason gives the whole restart: stop it with `tandem build stop`, end its open jobs with `tandem state recover --run ID`, archive it with `tandem state archive --run ID`, re-pair with a reviewer and hand off again. An archive refuses while a job is open, and names `tandem state recover` as the remedy.

```text
tandem-pair --reviewer claude --reviewer-model opus --reviewer-effort high
tandem build start ... --reviewer reviewer --reviewer-kind claude
```

`tandem-pair` starts a third pane, `reviewer`, split from the builder's, whenever the repository's `.tandem.json` has a `reviewer` block or `--reviewer KIND` is given. The flags win over the block for that pairing. It is launched as the `reviewer` role (`tandem agent launch-spec --role reviewer`), which gets the builder's unattended launch rules and pre-launch gates, but resolves from its own sources only: the `--reviewer*` flags, `HERDR_REVIEWER_*`, the `reviewer` block and the adapter's defaults. It never reads the `builder` block, `HERDR_BUILDER_*` or `defaults.builder_kind`. `tandem config resolve --role reviewer` shows where each value came from. The pair records it in `reviewer.json` beside `builder.json`, with its kind, name, model and autonomy. Either engine's `tandem build start` reads it from there, and `tandem reviewer show` prints it for the scripts. `tandem-unpair` closes the recorded reviewer pane before the builder's. It refuses while the reviewer is working or blocked, because its review would be lost, unless you pass `--force`.

In a run:

- Every review prompt goes to the reviewer pane. A task still goes to the builder. A notice, a verification and a recovery decision still go to the planner.
- Start refuses a reviewer pane that is the builder or the planner. The builder would review its own work, and the planner its own plan.
- The pane is observed by its own name. A result is taken only once the reviewer has finished its turn, and a `review_started` session is checked against the reviewer pane, not the builder's. The run never resets the pane. Each packet tells it to judge the task on its own, because it keeps earlier tasks in its context.
- The reviewer's passing review is the independent check. The planner is not asked to verify, and acceptance reads the reviewer's result where it would read the verification stage. The result counts only from a reviewer that is not the writer. If its conversation turns out to be the builder's or the planner's, the run pauses with `verification_identity`. If the pane has no result for an attempt, as with a waived review, the planner's verification still applies to a run started with an acceptance policy.
- Each review prompt registers a `reviewer` source under the reviewer's kind and conversation. A collaboration counts its tokens apart from the builder's, and its budget admits the launch under the reviewer's provider. The run report does not include them.

The plan review before a build goes to the reviewer pane too. `tandem-review` reads `reviewer.json` and sends the reset and the prompt to the pane it names, and `tandem-verdict` from that pane tells the planner "the reviewer reviewed". `tandem-review` writes `--role NAME`, the reviewer's recorded name, into the verdict command it asks for, and `--role` takes precedence over `HERDR_ROLE`: Codex runs its commands without the pane's environment, so a Codex reviewer's verdict used to say "the builder reviewed". A claimed role decides only where Herdr cannot: `tandem-verdict` and `tandem-handback --review` both ask `tandem agent self --expect NAME`, and in a pane whose socket Herdr answers on, Herdr's name for the pane decides. "Which pane is the reviewer" in guide/reviews.md has the whole rule. A review prompt whose turn ends at once with no model work is not sent, and `tandem-review` exits 8 with the pane's last lines. Unlike the run's reviews, a plan review starts in a fresh conversation. `tandem agent reset` types the reset command of the kind recorded for the pane it resets, so a Codex reviewer beside a Claude builder gets `/new`, not `/clear`. If the recorded reviewer has no pane, or `reviewer.json` cannot be read, `tandem-review` sends nothing and says to re-run `tandem-pair`. There is no flag that sends the review to the builder: `tandem-review --builder` and `tandem-verdict --role builder` refuse and say why, and with no `reviewer.json` nothing is sent. The verdict records the reviewer's kind and model from `reviewer.json` and `reviewed_by: reviewer`, besides the builder's kind and autonomy. The gate refuses with `no_reviewer`, `builder_review` (a record with no `reviewed_by`, as every pre-upgrade record has) or `reviewer` (kind changed, or model changed when both sides were measured). Effort is not compared.

A legacy build uses the reviewer pane as well. `tandem build start` refuses with exit 23 when `reviewer.json` records none. With review triage on, the bridge types each task's review prompt into the reviewer pane, and the reviewer answers with `tandem-handback --task N --review --verdict clean|changes`. In a Herdr pane the handback takes the pane's identity from Herdr, not from `--role`, and refuses if that lookup fails. `clean` accepts the task. `changes` sends the findings to the builder as a fix, for at most 3 rounds, then stops with `review_rounds`. A `changes` verdict on a task already over its input budget stops with `budget` and sends no fix. A reviewer that edits the task's files stops the task with `review_edited_tree`. See "Review triage" in guide/jev.md.

## Counters

`tandem build usage` reports `planner_turns`, kept apart:

- `verification`: independent verification prompts sent to the planner. These are substantive and are charged to the planner.
- `notifications`: the notice that the run is finished.
- `coordination`: planner turns spent only on polling, forwarding or approving an advance. A mailbox run asks none, so this stays 0.

The run report covers the run's model jobs only. The collaboration report adds planning, replacement runs and closeout. If a source is missing or unreadable, the total says it is partial and does not guess.

## Rolling back

Start new runs without `--review-delivery`. Runs already started finish under the protocol they recorded. The tables stay and nothing reads them.

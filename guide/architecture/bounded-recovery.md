# Bounded recovery

Status: opt-in, off by default. It is a second treatment, separate from [direct review handback](review-handback.md). A run that does not pass `--recovery-policy` behaves exactly as it did before: a failed check pauses the run and waits for a person, and a reviewer's request for changes takes the existing repair path.

## What it does

A task that fails is repaired automatically **once**, with the same plan and the same context. If the repair fails too, or the failure is not one the policy repairs, Tandem stops and asks the planner one concrete question. It never replans, never raises a budget and never loops.

```text
tandem build start ... --collaboration ID --recovery-policy policy.json
```

The policy file is `bounded-v1`. It can only narrow what bounded recovery does:

```json
{"schema_version": 1, "name": "bounded-v1",
 "automatic_repair": {"kinds": ["declared_local_check", "review_changes_requested"]},
 "max_automatic_repairs_per_work": 1, "escalate_to": "planner", "replanning": "none"}
```

A file that adds a kind, allows two repairs, escalates elsewhere or turns on replanning is refused at start. The policy is hashed into the run's configuration and cannot change while the run lives. It needs a collaboration, because the repair allowance lives in the collaboration's budget groups.

## Which failures are repaired automatically

Two kinds, and only when the failure names something to fix and the old builder has finished:

- `declared_local_check`: the project's declared check failed and left a complete result.
- `review_changes_requested`: a review (or the independent verification) asked for changes and has at least one open finding that names a file.

Everything else is escalated: a stuck builder, a review that judged the work failed, an unclassifiable failure, infra errors, missing prerequisites, external service failures and uncertain deliveries. No policy file can add them.

## One allowance per piece of work

The allowance belongs to an origin **budget group**, not to a run or a task name. When a run starts with a recovery policy, each of its tasks is bound to logical work automatically, named by the task's slug. A task whose slug already names work in the collaboration (a replacement run of the same plan) is bound to that work, so a spent allowance carries over. A slug whose work has since split into several pieces is left unbound, because minting a fresh allowance for it would reset a spent one; a person decides which piece is meant. A task bound to logical work draws on every group that work carries, so these cannot reset it:

- a replacement run that binds its task to the same work;
- a rename;
- a split (the pieces keep the parent's groups);
- a merge (the merged work carries all its origins and needs all of them).

Admission is one SQLite transaction. It checks the collaboration's stop generation, the run state and version, the writer lease and that the task is not accepted. It then consumes the allowance of every group the work draws on, or none, supersedes the failed attempt and creates the successor. A retry, a restart or a crash returns the same successor and consumes nothing twice. A task bound to no work, or bound to work that was split away, has no allowance to draw on and is escalated.

The successor starts from the task's **original tree**, not from the tree the failure left. Its final patch is therefore the whole task: the failed attempt's retained edits, any manual changes and its own work. The declared check, the builder review and (when the run requires it) the independent verification all run on that. The failed attempt gets no submitted snapshot it did not make, and a late result from it is rejected as stale. Dispatching the successor still passes the usual admission, so a collaboration stop or an exhausted budget prevents the new work even after the successor exists.

## One incident, one question

A failure that is not repaired becomes an incident. Its key comes from the collaboration, the work (or the task, when unbound), the failed attempt and the kind of failure, and it is written before the planner hears anything. A duplicate failure event, a restart or a replayed mailbox finds the same row and sends nothing more.

The planner gets one request. It holds the evidence (with the artifact to read in full), the repairs used per budget group, the collaboration's provider budgets, and the ways forward. It is sent while the run is paused and is reserved against the planner's registered source, so the turn is counted and a stop prevents it.

## Your options after an incident

1. Fix it in the checkout, then hand it back:

   ```text
   tandem build reconcile --actor NAME --reason "what you changed"
   ```

   Tandem snapshots the tree, records the edit as a manual intervention, resolves the incident and starts a new attempt from the task's original tree. Nothing is sent to a builder. The check and both review stages run again on the new tree; evidence from before the edit is not reused. A reconcile never creates or resets a repair allowance and never raises a budget. The task's attempt limit still applies.
2. Stop the run with `tandem build stop`.
3. Raise a budget explicitly with `tandem collaboration amend`. Resuming never does this.

A restart (`tandem build recover`) does not resume a run that is waiting for a decision.

## Counting

The decision request is a planner turn and is counted with the planner's source. Manual interventions are counted per run. The usage report keeps verification, notifications and coordination turns apart as before. Completion is announced once, after every task has passed every gate.

## Rolling back

Start new runs without `--recovery-policy`. A run keeps the policy it started with. The tables stay and nothing reads them.

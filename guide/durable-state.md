# Durable state

[Build engines](build-engines.md) explains how the legacy and durable engines differ.
[Durable runner operations](durable-runner-operations.md) is the operator's guide, and the
[rollout report](durable-runner-rollout-report.md) holds the current rollout decision and
its evidence. Durable runs stay opt-in.

`tandem state` inspects the durable store, and keeps and moves legacy build history.
`status` and `import-legacy --archive` never switch the build engine.
`import-legacy --active` does, for the one checkout it names.

    tandem state status --json                    # prints one JSON object, and writes nothing
    tandem state import-legacy --archive PATH     # keeps a finished build as archived history
    tandem state import-legacy --active DIR --quiescent
                                                  # moves a stopped legacy build into a durable run
    tandem state export --run ID --output PATH    # copies a run and every artifact it names
    tandem state recover --run ID                 # reconciles a run and reports where it is
    tandem state rebind --checkout ID --root PATH # points a checkout's state at a moved worktree
    tandem state gc [--apply]                     # lists unused artifacts, and --apply deletes them
    tandem state archive --run ID                 # turns a settled run into history, keeping all of it
    tandem state engine                           # prints which engine owns this relay, and refuses an unknown one

## status

`status` reports:

- whether a store exists, and its schema version;
- the current run, and why it is paused;
- how complete the recorded token usage is.

If a newer tandem wrote the store, `status` reports only its version and exits 23.

## import-legacy --archive

`import-legacy --archive` takes an archive made by `tandem build archive`. You rarely run
that yourself, because `tandem-unpair`, a handoff that replaces a build, and a durable start
over a finished legacy build all run it for you. The import checks every byte, stores each
file as an artifact, and records one archived run. Importing the same content again, from
any path, returns the same run.

The import keeps what the legacy build recorded. It marks as unknown what the legacy build
never recorded: snapshots, check certificates and review results. It never counts an
imported task as accepted.

## import-legacy --active

`import-legacy --active DIR --quiescent` moves a stopped legacy build onto the durable
engine. DIR is any folder in the checkout. `--quiescent` is your statement that nothing is
writing, and tandem checks it anyway. It refuses, and writes nothing, in any of these cases:

- the build is running or finished;
- the build's record changes while tandem reads it;
- the builder's pane is not idle or done, or tandem can't see it;
- the plan is not the one the build started from;
- the checkout already has a durable run or a writer lease, even an expired one.

Otherwise it archives the build as a history run, starts a new run over the whole plan, and
renames the legacy `build.json` to `build.json.imported`. Each task the legacy engine
finished comes across with one accepted attempt. That attempt has no snapshot, patch, check
or review, because the legacy engine never recorded them in a form tandem can check, so its
check and review stay pending. The first unfinished task starts as a new attempt, from a
snapshot of the tree as it is now. The result lists what stayed unresolved.

## export

`export` writes a new folder, and never overwrites an existing one. The folder holds
`manifest.json`, every artifact the run's rows name, the files of its snapshots and both
sides of its patches, in the same layout as an artifact store. `export` checks each copy and
writes `COMPLETE` last. It lists any delivery whose outcome is unknown under `unresolved`.

To roll back from the durable engine:

1. Stop the run, and let its jobs settle.
2. Export it.
3. Keep the durable store.
4. Start a separate legacy run if you need one.

tandem never reads a durable run as a legacy one.

## recover and rebind

`recover` settles the run's pending and uncertain effects and prints the run's state. It
ends any builder job whose attempt was already accepted or replaced. On a stopped run, it
ends every builder job still open. A pause that needs a person is a successful result,
with `state` set to paused and a `pause_code`.

`rebind` needs an idle checkout: no running or stopping run, no held lease, and no open job
or delivery. The new path must be in the same git folder. If the repository itself moved,
pass `--repository-moved`, which works only once the old git folder is gone. A refusal that
needs a person exits 23, with the reason on stderr.

## gc and archive

`gc` prints one JSON object with these fields:

- `candidates`: artifacts that no row, reference or snapshot names;
- `deleted` and `retained`;
- `missing` and `corrupt`: referenced artifacts that are gone or fail their hash. These are
  evidence, so they are never candidates.

Without `--apply`, `gc` only reads. `--apply` needs an idle checkout, with no running or
stopping run and no writer lease, expired or not. It takes the exclusive operation lock,
which it can't get while anything is publishing. It then works out the references again,
and deletes each candidate's row before its file. It never deletes by age, and never
deletes a run.

`archive` refuses while a run is running or stopping, or has an open job or delivery. It
renames the relay's marker to `active-run.<run id>.archived.json`.

## Closing a durable run

`tandem-unpair` signs a durable run off on the run's own evidence (see
[step 7](walkthrough.md#7-the-builder-hands-back-and-the-planner-closes-the-pair)). Then
it stops the run and waits for it to settle. `TANDEM_UNPAIR_SETTLE_POLLS` sets the number of
one-second polls, and the default is 10. Last, it archives the run and removes the pane, the
bridge and the relay. The run's rows and artifacts stay in the state home.

If the run doesn't settle, or the archive is refused, tandem closes and deletes nothing.
`--force` closes the panes anyway and keeps the state.

`tandem-bridge`, `tandem-handoff` and `tandem-unpair` run `tandem state engine` first. It
refuses a relay with exit 23, before reading or writing anything there, in these cases:

- the relay's `active-run.json` names an engine or protocol other than 2;
- tandem can't read the marker;
- the marker sits beside a legacy build.

To fix it, update your install with `install.sh`.

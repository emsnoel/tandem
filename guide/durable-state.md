# Durable state

What separates the legacy and durable build engines is in
[guide/build-engines.md](build-engines.md). The operator's guide is
[guide/durable-runner-operations.md](durable-runner-operations.md);
the current rollout decision and its evidence are in
[guide/durable-runner-rollout-report.md](durable-runner-rollout-report.md). Durable runs
stay opt-in.

`tandem state` looks at the durable store, preserves legacy history and moves it.
`status` and `import-legacy --archive` never switch the build engine. `import-legacy --active`
does, for the one checkout it names. Durable runs stay opt-in.

    tandem state status --json                    # one JSON object; reads, never writes
    tandem state import-legacy --archive PATH     # keep a finished build as archived history
    tandem state import-legacy --active DIR --quiescent
                                                  # carry a stopped legacy build into a durable run
    tandem state export --run ID --output PATH    # copy a run and every artifact it names
    tandem state recover --run ID                 # reconcile a run, report where it is
    tandem state rebind --checkout ID --root PATH # point a checkout's state at a moved worktree
    tandem state gc [--apply]                     # report unreferenced artifacts; --apply deletes them
    tandem state archive --run ID                 # make a settled run history, keep all of it
    tandem state engine                           # which engine owns this relay; refuses an unknown one

`status` reports whether a store exists, its schema version, the current run and why it is
paused, and how complete the recorded token usage is. A store written by a newer tandem is
reported by version only and exits 23. `import-legacy --archive` takes an archive made by
`tandem build archive` (which `tandem-unpair`, a replacing handoff and a durable start over a
finished legacy build run for you), verifies
every byte, stores each file as an artifact and records one archived run. Importing the same
content again, from any path, returns the same run. It keeps what the legacy build recorded
and marks what it never did (snapshots, check certificates, review results) as unknown;
imported tasks are never counted as accepted.

`import-legacy --active DIR --quiescent` (DIR is any directory in the checkout) moves a
**stopped** legacy build onto the durable engine. `--quiescent` is your statement that
nothing is writing, and tandem checks it anyway. It refuses, writing nothing, if the build is
running or finished, its record changes while it looks, the builder's pane is not idle or
done (or cannot be seen), the plan is not the one the build started from, or the checkout
has a durable run or a writer lease, even an expired one. Otherwise it archives the build as
a history run, starts a new run over the whole plan, and sets the legacy `build.json` aside as
`build.json.imported`. Tasks the legacy engine finished are carried with one accepted attempt
each. That attempt has no snapshot, patch, check or review, because the legacy engine never
recorded them in a form that can be checked, and its check and review stay pending. The first
unfinished task starts as a new attempt from a snapshot of the tree as it is now. The result
lists what stayed unresolved.

`export` writes a new directory (it never overwrites one) holding `manifest.json`, every
artifact the run's rows name, the files of its snapshots and the sides of its patches, in the
layout of an artifact store. Each copy is verified, and `COMPLETE` is written last. A delivery
whose outcome is unknown is named in `unresolved`. To roll back from the durable engine: stop
the run, let its jobs settle, export it, keep the durable store, and start a separate legacy
run if you need one. A durable run is never reinterpreted as a legacy one.

`recover` reconciles the run's pending and uncertain effects, ends any builder job whose attempt is already accepted or replaced (and, on a stopped run, every builder job still open), and prints its state. A pause
that needs a person is a successful result with `state` paused and a `pause_code`. `rebind`
needs the checkout idle (no running or stopping run, no held lease, no open job or delivery)
and the new path in the same git directory; a repository that itself moved needs
`--repository-moved`, and only works once the old git directory is gone. A refusal that needs
a person exits 23 with the reason on stderr.

`gc` prints one JSON object: `candidates` (artifacts no row, reference or snapshot names),
`deleted`, `retained`, and `missing` and `corrupt` for referenced artifacts that are gone or
fail their hash. Those two are never candidates, since they are evidence. Without `--apply`
it only reads. `--apply` needs an idle checkout (no running or stopping run, no writer
lease, expired or not), takes the exclusive operation lock (refused while anything is
publishing), works out the references again after taking it, and deletes a candidate's row
before its file. It never deletes by age and never deletes a run. `archive` refuses while a
run is running or stopping or has an open job or delivery, and sets the relay's marker aside
as `active-run.<run id>.archived.json`.

`tandem-unpair` on a durable run signs it off on the run's own evidence (see
[step 7](walkthrough.md#7-the-builder-hands-back-and-the-planner-closes-the-session)), stops it, waits for it to settle (`TANDEM_UNPAIR_SETTLE_POLLS`
one-second polls, default 10), archives it and then removes the pane, the bridge and the relay.
The run's rows and artifacts stay in the state home. If the run does not settle, or an archive
is refused, nothing is closed or deleted; `--force` closes anyway and keeps the state.
`tandem-bridge`, `tandem-handoff` and `tandem-unpair` first run `tandem state engine`. A relay
whose `active-run.json` names an engine or protocol other than 2, a marker that cannot be
read, or a marker beside a legacy build is refused with exit 23 before anything is read from
or written to it. The remedy is to update the install with `install.sh`.

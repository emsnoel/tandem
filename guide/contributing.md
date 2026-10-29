# Working on tandem

## Tests

```bash
./tests/run.sh
./tests/run.sh --with-clis   # additionally probe installed agent CLI flags
```

Shell-syntax checks over `bin/`, then `go build ./...` and `go test ./...`, then every
`tests/test-*.sh`. Exit 0 means the harness is sound. This is also the `check_command` in `.tandem.json`, so `tandem-unpair` runs it
before it will close a builder pane.

That last point constrains what a test here may do. **`tandem-unpair` runs the check while
the builder is still live**, so a test that wanted a free pane, a running Herdr server, or
sole ownership of `~/.codex/config.toml` would deadlock every teardown — it would fail
precisely because the thing it is gating on has not been torn down yet. Tests are
therefore hermetic: `herdr` is stubbed onto `PATH` and `CODEX_HOME` is redirected into a
temp dir, so they run with no server, no panes, no network, and no writes outside
`mktemp` or the repo. The one in-repo exception is `.gocache/`, where `GOCACHE` and
`GOMODCACHE` are pinned: the builder's sandbox denies `~/Library/Caches/go-build`
outright, and a cache there would break the rule above anyway. It is gitignored, so it
cannot trip the tree-change gate.

`test-relay-ignore.sh` lifts `ensure_relay_ignored()` out of `tandem-pair` with `sed` and
runs that, rather than keeping its own copy of the logic — a test asserting against a
duplicate of the code it is testing proves only that the duplicate is self-consistent.

`test-staleness.sh` asserts the installed `tandem` matches the Go sources, which is what
makes the copied binary safe.

**Run `./install.sh` from the branch under test before any live check.** The `bin/`
commands are symlinks into the checkout, so they are always current, but `tandem` itself
is a compiled copy and stays at whatever was last installed. A live pair run against a
stale binary tests old Go code behind new scripts. `tandem version` prints the first 12
characters of the source hash; `bin/lib/source-hash.sh .` prints the whole of the one it
should match.

`test-dispatch.sh` covers `tandem <verb>`, and is bash because it cannot be Go: dispatch
ends in `execve`, so a Go test calling it would never return to assert anything. It builds
its own binary, invents a delegate that exits with whatever it is told, and checks each
code in the exit-code vocabulary ([When tandem refuses](refusals.md)) arrives unchanged — a wrapper that observed a child and
re-exited would flatten 23 to 1 and turn a gate refusal into an ordinary error. It also
pins that a compiled subcommand is never shadowed by a stray `tandem-version` on `PATH`.

`test-cli-drift.sh` is opt-in through `--with-clis`. It probes flags on installed agent
CLIs, but stays out of the default teardown check so machines without those CLIs remain
supported.

**Measuring token use.** `tandem usage ROLLOUT.jsonl...` reads Codex session logs and
prints, per phase (one per prompt handed over): calls, the context size of the first call,
peak, input, cached and output tokens, `write_stdin` polls, and the `yield_time_ms` values
requested. Pass the review session and the build session together — the build now runs in
its own. Logs live under `~/.codex/sessions/YYYY/MM/DD/`. Codex only; a durable run's
`tandem build usage` also reads Claude and Copilot builders.

`./tests/run.sh` unsets `HERDR_DECISION`, `HERDR_JUDGE` and `TYPESAFE_API_KEY` first: a
machine that turned Jev on in its shell profile must not change what the suites see, since
`tandem-unpair` runs the same check in that environment.

The Go half carries the decisions. `internal/gate` holds the teardown verdicts as
pure functions — codes 4, 5, 6, 8 and 9; exit 7 is the project's own check command
failing, which bash runs and reports — and is forbidden — transitively, checked with `go list -deps` — from
reaching `os/exec` or the herdr facade, so the gates can be tested with no terminal, no
server and no filesystem. `internal/hooks` holds the Codex hook-trust classification;
it replaced a 20-assertion bash suite that lifted `codex_hook_trust()` the same way
`test-relay-ignore.sh` does, and carries the same 20 assertions as `t.Run` subtests,
plus one for a machine still holding a pre-rename baseline.

## Layout

```
bin/          the twelve commands — bash, symlinked by install.sh, live-editable
cmd/tandem/   the compiled core's entry points: its own subcommands, the handover to
              the bash ones (dispatch.go), and exit codes
internal/
  agent/      the adapter seam: normalised vocabulary -> each CLI's native flags. Pure;
              a dependency-closure test forbids exec, net and herdr in its reach
  agent/trust/  launch-time gates that touch another tool's own config — the I/O half,
              kept out of agent/ so that purity test stays meaningful
  config/     repo and user config, precedence with provenance, kind approvals
  herdr/      execs the herdr CLI for actions, and reads the socket's event stream for
              every wait on an agent's status, session or departure
  mailwatch/  wakes a waiter when a message lands in a relay queue or a named file
              changes (fsnotify); a wake is a hint, the caller still lists the folder
  gate/       the teardown verdicts, pure — no exec, no herdr, no terminal
  hooks/      Codex hook-trust classification, pure
  mailbox/    handback report snapshots; reads files, never shells out
  vcs/        `git status --porcelain`, and the per-file hashes that tell which files a
              build task changed
  plan/       splits a plan into one brief per task, pure
  build/      the legacy engine: the per-task build record, the decision after each
              task, and the ledger, report and planner message rendered from it
  check/      runs the project's check with output to a log, and summarises it
  paths/      where tandem's own files live, and the one place that knows their
              pre-rename names. `tandem paths` exposes it so bash shares the rule
  exitcode/   the shared exit-code vocabulary
  rollout/    Codex rollout-log parsing for `tandem usage`, pure
  sourcehash/ the source fingerprint install.sh stamps into the binary
  decision/   staffing advice from Jev, pure; evidence/ builds its redacted input
  triage/     whether a finished task still needs a review (Jev), off by default
  # the durable engine, see guide/build-engines.md
  controller/ start, tick, recover and stop; runs the check and binds it to a tree
  workflow/   the run's pure records, transitions and errors; imports nothing else
  state/      the versioned SQLite store per checkout, leases and fences (pure-Go driver)
  runner/     delivers the controller's prompts under a fenced lease, and says honestly
              whether each was sent, not sent, or may have been sent
  snapshot/   captures the tree at a moment; a task's patch is the diff of two captures
  artifact/   content-addressed bytes in the state home, outside the repository
  taskcontext/ the one bounded prompt a task gets, and a manifest of what it left out
  memory/     which remembered decisions a later task may be shown, and why
  usageledger/ normalises provider logs into per-event usage for collaborations
  evalrow/    the result row a scripted scenario run writes, legacy and durable alike
tests/        bash suites; also runs go build and go test. `--with-clis` adds a probe of
              the installed agent CLIs for flag drift, kept out of the default run
              because check_command must pass on a machine with none of them
tests/lib/    stubs shared by the handoff suites
guide/        design notes, the build engines, operations, versioning and releasing
tools/        maintainer scripts: package-release.sh builds the release tarballs
```

The split follows one rule: **Go decides, bash explains.** Go returns a verdict and at
most a machine-readable token; every word a human reads stays in the bash script that
prints it. `guide/design.md` says why. The exceptions are sentences two callers must say
identically: the reset sentences (`tandem agent explain-reset`), and the build's report and
planner message, which `tandem build` writes because the bridge and the tests must agree on
every word.

## Design notes

`guide/design.md` explains why the harness is shaped this way — the sandbox constraint that
forces the mailbox, why answers are files rather than scraped panes, the invariants that
keep it safe to edit, and the twenty-eight findings (plus four wrong predictions) behind them.
Read it before changing the relay or the launch prompt.

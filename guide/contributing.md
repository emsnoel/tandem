# Working on tandem

## Tests

```bash
./tests/run.sh
./tests/run.sh --with-clis   # also checks the flags of installed agent CLIs
```

`tests/run.sh` checks the shell syntax of `bin/`, runs `go build ./...` and
`go test ./...`, then runs every `tests/test-*.sh`. Exit 0 means all of it passed. It is
also the `check_command` in tandem's own `.tandem.json`, so `tandem-unpair` runs it before
it closes a builder pane.

That limits what a test may do. **`tandem-unpair` runs the check while the builder is
still running.** A test that needed a free pane, a running Herdr server, or sole use of
`~/.codex/config.toml` would block every teardown, because it would wait on the thing the
teardown hasn't closed yet. So the tests are self-contained. They put a stub `herdr` on
`PATH` and point `CODEX_HOME` at a temp folder. They run with no server, no panes and no
network, and write only under `mktemp` or the repo.

The one exception inside the repo is `.gocache/`, where tandem pins `GOCACHE` and
`GOMODCACHE`. The builder's sandbox blocks `~/Library/Caches/go-build`, and a cache there
would break the rule above anyway. `.gocache/` is in `.gitignore`, so it can't trip the
tree-change gate.

The tests need Go 1.26 or later and `jq`, which they use to write fixtures and to read
what the scripts wrote. tandem itself never needs `jq`. `test-script-limits.sh` fails if a
file in `bin/` or `install.sh` calls it, and `test-install-no-jq.sh` installs on a `PATH`
without it.

`test-relay-ignore.sh` copies `ensure_relay_ignored()` out of `tandem-pair` with `sed` and
runs that copy, instead of keeping its own version of the logic. A test that checks a
duplicate of the code proves only that the duplicate agrees with itself.

`test-staleness.sh` checks that the installed `tandem` matches the Go sources, which is
what makes the copied binary safe.

**Run `./install.sh` from the branch under test before any live check.** The `bin/`
commands link into the checkout, so they are always current. But `tandem` itself is a
compiled copy, and stays at whatever you last installed. A live pair run with a stale binary
tests old Go code behind new scripts. `tandem version` prints the first 12 characters of the
source hash, and `bin/lib/source-hash.sh .` prints the full hash it should match.

`test-dispatch.sh` tests `tandem <verb>`. It is a bash test because it can't be a Go test.
Dispatch ends in `execve`, so a Go test that called it would never get control back to check
anything. The test builds its own binary and a fake delegate script that exits with
whatever code it is told. It checks that each code in the exit-code list
([When tandem refuses](refusals.md)) arrives unchanged. A wrapper that watched a child and
exited after it would turn 23 into 1, and a gate refusal into an ordinary error. The test
also checks that a stray `tandem-version` on `PATH` never hides a compiled subcommand.

`test-cli-drift.sh` runs only with `--with-clis`. It checks flags on the installed agent
CLIs. It stays out of the default check so that machines without those CLIs still pass.

`./tests/run.sh` unsets `HERDR_DECISION`, `HERDR_JUDGE` and `TYPESAFE_API_KEY` first. A
machine that turned Jev on in its shell profile must not change what the tests see, because
`tandem-unpair` runs the same check in that environment.

The Go code makes the decisions. `internal/gate` holds the teardown verdicts, codes 4, 5, 6,
8 and 9, as pure functions. Exit 7 is the project's own check failing, which bash runs and
reports. A test runs `go list -deps` to make sure `internal/gate` never reaches `os/exec`
or the herdr package, even indirectly. So the gates can be tested with no terminal, no
server and no filesystem.

`internal/hooks` holds the Codex hook-trust classification. It replaced a bash test file
with 20 checks, which copied `codex_hook_trust()` out of the script the same way
`test-relay-ignore.sh` does. The Go tests keep the same 20 checks as `t.Run` subtests, plus
one for a machine that still has a baseline from before the rename.

## Measuring token use

`tandem usage ROLLOUT.jsonl...` reads Codex session logs. For each phase, meaning each
prompt handed over, it prints:

- the number of calls;
- the context size of the first call, and the largest;
- input, cached and output tokens;
- `write_stdin` polls, and the `yield_time_ms` values requested.

Pass the review session and the build session together, because the build runs in its own
session. Codex keeps the logs under `~/.codex/sessions/YYYY/MM/DD/`. `tandem usage` reads
Codex logs only. For a durable run, `tandem build usage` also reads Claude and Copilot
builders.

## Layout

```
bin/          the twelve commands, in bash. install.sh links them, so edits take effect at once
cmd/tandem/   the compiled binary's entry points: its own subcommands, the handover to
              the bash commands (dispatch.go), and the exit codes
internal/
  agent/      maps the shared settings onto each CLI's own flags. Pure: a test forbids
              exec, net and herdr anywhere in its dependencies
  agent/trust/  launch checks that touch another tool's config. This is the I/O half,
              kept out of agent/ so that the purity test stays meaningful
  config/     repo and user config, which source wins and why, and kind approvals
  herdr/      runs the herdr CLI for actions, and reads the socket's event stream for
              every wait on an agent's status, session or exit
  mailwatch/  wakes a waiter when a message lands in a relay queue or a named file
              changes (fsnotify). A wake is a hint, and the caller still lists the folder
  gate/       the teardown verdicts. Pure: no exec, no herdr, no terminal
  hooks/      Codex hook-trust classification. Pure
  mailbox/    snapshots of handback reports. Reads files, and never runs commands
  vcs/        `git status --porcelain`, and the per-file hashes that show which files a
              build task changed
  plan/       splits a plan into one brief per task. Pure
  build/      the legacy engine: each task's build record, the decision after each
              task, and the ledger, report and planner message made from it
  check/      runs the project's check with its output in a log, and summarises it
  paths/      where tandem's own files live, and the one place that knows their
              old names. `tandem paths` exposes it so the bash commands use the same rule
  exitcode/   the shared exit codes
  rollout/    reads Codex rollout logs for `tandem usage`. Pure
  sourcehash/ the source hash install.sh stamps into the binary
  decision/   staffing advice from Jev. Pure. evidence/ builds its redacted input
  triage/     whether a finished task still needs a review (Jev). Off by default
  # the durable engine, see guide/build-engines.md
  controller/ start, tick, recover and stop. Runs the check and ties it to a tree
  workflow/   the run's pure records, transitions and errors. Imports nothing else
  state/      the versioned SQLite store for each checkout, with leases and fences (Go driver)
  runner/     delivers the controller's prompts under a fenced lease, and reports whether
              each one was sent, not sent, or may have been sent
  snapshot/   captures the tree at one moment. A task's patch is the diff of two captures
  artifact/   bytes stored by content hash in the state home, outside the repository
  taskcontext/ the one size-limited prompt a task gets, and a list of what it left out
  memory/     which remembered decisions a later task may see, and why
  usageledger/ turns provider logs into usage per event
  evalrow/    the result row a scripted test run writes, for both engines
tests/        bash test files. run.sh also runs go build and go test. `--with-clis` adds a
              check of the installed agent CLIs' flags, kept out of the default run
              because check_command must pass on a machine without them
tests/lib/    stubs shared by the handoff tests
guide/        the user guides, design notes, build engines, operations, versioning and releasing
tools/        maintainer scripts. package-release.sh builds the release tarballs
```

The code follows one rule: **Go decides, bash explains.** Go returns a verdict, and at most
a token a machine reads. Every word a person reads stays in the bash script that prints it.
`guide/design.md` explains why. The exceptions are sentences that two callers must say
word for word the same: the reset sentences (`tandem agent explain-reset`), and the build's
report and planner message. `tandem build` writes those, because the bridge and the tests
must agree on every word.

## Design notes

[Design notes](design.md) explains why tandem is built the way it is. It covers the sandbox
limit that forces the mailbox, why answers are files instead of scraped panes, the rules
that keep tandem safe to edit, and the 28 findings and 4 wrong predictions behind them.
Read it before you change the relay or the launch prompt.

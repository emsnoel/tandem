# Commands

Every command has two spellings that do the same thing:

```bash
tandem pair          # what you type
tandem-pair          # the script itself, which keybindings and other scripts call
```

`tandem <verb>` is the compiled binary handing over to `tandem-<verb>` on `PATH`. It uses
`execve`, not a subprocess, so the script replaces `tandem` and its exit code arrives
unchanged. That matters because tandem's exit codes carry meaning (see
[When tandem refuses](refusals.md)). The binary finds the script with a `PATH` lookup, not a
compiled list, so a stale `tandem` still reaches a script added or renamed after it was
built. Run `tandem` with no arguments to see both kinds of command.

The keybindings in `config.toml`, the launch prompt's instructions to the builder, and the
scripts that call each other all use the `tandem-<verb>` form. Nobody reads the output on
those paths, so they skip the extra hop.

| Command                  | Does |
| ------------------------ | ---- |
| `tandem-pair`            | Names the current Claude agent `planner`. Starts or adopts the configured `builder` in a sibling pane and the configured `reviewer` beside it, and starts the relay bridge. Safe to run again |
| `tandem-status [plan]`   | Prints the pair's state: the builder's identity, the plan's review state, mailbox activity, and whether the bridge is running. Changes nothing, and works whether or not a builder is paired |
| `tandem-handoff [plan]`  | Sends the builder a launch prompt that names a plan. Without an argument it picks the most recently reviewed plan, or else the newest plan in the plans folder. Refuses with exit 24 until the plan passes review. `--dry-run` sends nothing. `--context-limit N` sets a durable run's prompt limit per task |
| `tandem-review [plan]`   | Asks the reviewer pane to review a plan, and changes nothing. With no reviewer recorded it sends nothing and tells you to declare a `reviewer` block or run `tandem-pair --reviewer KIND`. Picks the plan the same way as `tandem-handoff` |
| `tandem-verdict`         | Returns a review to the planner with a verdict. The reviewer pane runs it at the end of a review, and tandem refuses it from any other pane (see [Which pane is the reviewer](reviews.md#which-pane-is-the-reviewer)). `tandem-review` writes `--role NAME`, the reviewer's recorded name, into the command, because Codex runs commands without the pane's `HERDR_ROLE` |
| `tandem-ask <agent> "q"` | Asks the other agent a question and prints the reply. Works from either side |
| `tandem-relaylog`        | Prints the history of every `tandem-ask` question that got an answer. Takes `--limit N` and `--json`. The history stays after `tandem-unpair` |
| `tandem-handback`        | Hands finished work back to the planner with a report. The builder runs it when it is done |
| `tandem-unpair`          | Closes the builder pane and the reviewer pane that `tandem-pair` recorded in `reviewer.json`, and refuses while the reviewer is mid-review. Stops the bridge and removes the mailbox, or both mailboxes when a pair made from a subdirectory has two. Refuses until every handback is signed off and the check passes. `--keep-relay` keeps the mailbox. `--clean-trust` removes the trust entries tandem added for the builder's kind and the reviewer's |
| `tandem-bridge`          | Delivers questions, handbacks and review verdicts from a sandboxed agent. `tandem-pair` starts it |
| `tandem-project`         | Prints the project's resolved settings as JSON |
| `tandem-init`            | Writes `.tandem.json` and adds the relay mailbox and the question history folder to `.gitignore` |

`tandem-pair` also takes these flags for a single pairing:

- `--builder KIND`, `--builder-model`, `--builder-effort` and `--builder-autonomy` set the builder.
- `--reviewer KIND`, `--reviewer-model` and `--reviewer-effort` override the `reviewer` block in
  `.tandem.json`.
- `--approve-kind KIND` approves a kind the repository asked for. Repeat it to approve the
  builder's and the reviewer's kinds in one run.
- `--skip-doctor` pairs without running `tandem doctor` first.

You need a reviewer. Every plan review and every task review goes to the reviewer pane, on
both build engines, so the builder can run a cheap model and the reviewer a strong one.
With no `reviewer` block and no `--reviewer`, `tandem doctor` reports a failure and
`tandem-pair` stops before it opens a pane. `tandem-pair --skip-doctor` pairs without one, but
reviews and builds refuse until you declare a reviewer. See
[Choosing an agent](configuration.md#choosing-an-agent) and `guide/architecture/review-handback.md`.

## Checking your setup

`tandem doctor` checks this machine and the current repository for everything that would stop
a pairing. It prints each problem with the command or step that fixes it, and it writes
nothing. Run it from the repo you want to pair in. It uses the git toplevel as the repo, the
same as `tandem-pair`.

It checks these things:

- Herdr is 0.9.3 or later, its socket answers, and this shell runs inside a Herdr pane.
- Herdr's integration is installed for each agent kind this repo uses.
- Each of those agent CLIs is on `PATH`, and prints a version.
- Each kind that `.tandem.json` asks for is one you approved for this repo.
- Each agent's trust dialog for this folder is already accepted.
- A reviewer is declared.
- Each role's model, effort and autonomy map to its CLI's flags.

Each line reads `ok`, `warn` or `FAIL`. A warning doesn't stop a pairing. With
`HERDR_SKIP_HOOK_CHECK=1` set, `tandem-pair` launches past a trust dialog, so doctor reports
that dialog as a warning instead of a failure.

A builder or reviewer that already runs needs no launch, so doctor skips what `tandem-pair`
skips for it. For a running builder that is its trust checks. For a running reviewer it is
also its approval and launch settings. Doctor finds them by the names `tandem-pair` gives
them (`HERDR_BUILDER_NAME` and `HERDR_REVIEWER_NAME`, `builder` and `reviewer` by default).

`tandem doctor` exits 0 when nothing fails and 30 when something does. `--json` prints the same results as
JSON: a `results` list with `group`, `name`, `status`, `detail` and `fix` for each check, and
`failures` and `warnings` counts. `--problems` prints only the checks that failed or warned,
and nothing when all of them pass. It leaves `--json` output unchanged.

`tandem doctor` takes the kind flags `tandem-pair` takes: `--builder`, `--builder-model`,
`--builder-effort`, `--builder-autonomy`, `--reviewer`, `--reviewer-model` and
`--reviewer-effort`. `--cwd DIR` checks another folder. `tandem-pair` runs `tandem doctor`
with `--problems` and its own flags before it opens any pane. It stops if doctor exits 30, and
otherwise shows any warnings and carries on.

## Subcommands of the binary

The scripts call these subcommands. `agent watch` and the bridge read Herdr's event stream
on its socket. `build wait` reads the run's ledger, and `pane wait-shell` reads the pane's
process info.

| Subcommand | Does |
| ---------- | ---- |
| `tandem agent watch [NAME...] [--root DIR] [--mailbox DIR] --timeout MS` | Waits for one of the named agents to change status. Prints `changed<TAB>NAME<TAB>STATUS` and exits 0, or prints `timeout` and exits 0. Exits 27 with `agent_exited<TAB>NAME` when an agent is gone, and 3 when the stream breaks or Herdr is unreachable. `--root` adds the durable run's builder and reviewer. `--mailbox` also returns on a message file in the relay's queues (`queued<TAB>PATH`), a change to its run marker (`run<TAB>PATH`), or a write to the run's state (`nudge<TAB>PATH`). It exits 1 when the file watcher cannot start |
| `tandem agent self [--expect NAME [--claimed ROLE] [--timeout MS]]` | Prints the name of the agent in this pane (`HERDR_PANE_ID`), or nothing. With `--expect`, it prints whether this shell is the reviewer pane NAME instead: `accept` or `refuse`, a reason and a detail, separated by tabs. See [Which pane is the reviewer](reviews.md#which-pane-is-the-reviewer) |
| `tandem agent prompt NAME TEXT --as KIND [--wait-unblocked MS]` | Sends the prompt the way KIND is delivered and confirms the turn ran. KIND is task, review, handback, verdict, question or notice, and `cmd/tandem/deliver.go` has one row of checks for each. The planner's kinds wait up to 2 minutes for a blocked agent, or for a pane showing a subagent or a list, before typing. `--wait-unblocked` sets that wait in milliseconds and needs `--as`. Every `tandem agent prompt` refuses such a pane with `agent_input_elsewhere` (see [A pane showing a subagent or a list](refusals.md#a-pane-showing-a-subagent-or-a-list)) |
| `tandem build wait --root DIR (--until STATE)... \| --until-not STATE --timeout MS [--tick]` | Prints the run's status and exits 0 once it reaches a wanted state, or 23 at the timeout. `sent` matches once the current attempt's prompt went out. `--tick` runs a tick before each check |
| `tandem build tick --root DIR --observe-agents` | After the tick, pauses the run with code `agent_exited` if Herdr reports its builder or reviewer gone. `tandem build recover` resumes it once the agent is back |
| `tandem agent planner [--pane ID] [--kind KIND]` | Picks the planner's pane: the one named, else the calling pane if it runs KIND (default `claude`), else the only agent of KIND. Prints its pane, cwd, tab and name as NUL-separated fields. Exits 31 when no agent of KIND runs, and 32 when several do, printing pane, name and cwd for each |
| `tandem agent adoptable --exclude-pane PANE [--tab TAB] [--kind KIND]` | Prints the pane and kind of the one unnamed agent `tandem-pair` may adopt as the builder, as NUL-separated fields, or nothing when there is none or more than one |
| `tandem agent kind NAME` | Prints the kind Herdr detected for the agent called NAME. Exits 10 when no agent has that name |
| `tandem pane split ...` | Splits a pane through Herdr and prints the new pane's id |
| `tandem pane split-direction PANE` | Prints `right` for a pane wider than twice its height and `down` otherwise, then its width and height, as NUL-separated fields. Exits 10 when Herdr gives no size |
| `tandem tab label TAB` | Prints `default` while the tab still has Herdr's numbered label, and `named` once a person has named it |
| `tandem config record-builder --relay DIR ...` | Resolves the builder with `config resolve`'s flags and writes `builder.json`. Prints its kind, autonomy and JSON as NUL-separated fields. `--dry-run` writes nothing. Exits 33, writing nothing, when the builder resolves with no kind or no autonomy |
| `tandem reviewer record --relay DIR --kind K --name N --autonomy A ...` | Writes `reviewer.json`. A running reviewer keeps the earlier record's model only for the same kind and name. Prints the model and the JSON as NUL-separated fields |
| `tandem pane wait-shell PANE --timeout MS` | Waits until the pane's foreground process is its shell. Exits 23 at the timeout and 3 when Herdr is unreachable |

`tandem config resolve`, `tandem agent launch-spec` and `tandem agent prelaunch` print JSON.
With `--fields` they print NUL-separated fields for bash instead, in the order each verb's
help text names. `tandem-pair` reads them that way, so it needs no `jq`.

Between passes the bridge waits on `tandem agent watch --mailbox`, and adds `--root` while a
durable run is active. A command that changes rows in the run's state leaves a `nudge` file
in the state root when it closes the store. That way a planner command such as
`tandem build stop` or `review-record` wakes the bridge at once.
`HERDR_BRIDGE_BACKSTOP_SECONDS` (default 30) sets the longest the bridge waits if it misses a
file event. `HERDR_BRIDGE_POLL_SECONDS` (default 1) sets the pause before the next pass after a
failed watch. The bridge writes `$MAILBOX/bridge.ready` once it is running, and
`tandem-pair` waits for that file.

## Keybindings

`prefix+i` pairs, `prefix+shift+h` hands off, and `prefix+a` toggles the agents. These come
from `config.toml`, so you only have them if you took the example config or copied those
entries across. `install.sh` never overwrites an existing config. The commands work
without the keybindings.

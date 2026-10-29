# Commands

Every command has two spellings that do exactly the same thing:

```bash
tandem pair          # what a human types
tandem-pair          # the script itself, and what keybindings and scripts use
```

`tandem <verb>` is the compiled binary handing over to `tandem-<verb>` on `PATH`. It is
`execve`, not a subprocess, so the script replaces `tandem` entirely and its exit code —
which for this harness carries meaning, see [When tandem refuses](refusals.md) — arrives unchanged.
Routing is a `PATH` lookup rather than a list compiled into the binary, so a `tandem` that
has gone stale still reaches a script that was added or renamed after it was built. Run
`tandem` with no arguments to see both families.

Machine-facing callers — the keybindings in `config.toml`, the launch prompt's instructions
to the builder, and the scripts calling each other — use the direct `tandem-<verb>` form.
One less moving part on paths where nothing is reading the output.

| Command                 | Does                                                                                                                           |
| ----------------------- | ------------------------------------------------------------------------------------------------------------------------------ |
| `tandem-pair`            | Names the current Claude agent `planner`, starts or adopts the configured `builder` in a sibling pane and the configured `reviewer` beside it, starts the relay bridge. Idempotent |
| `tandem-status [plan]`  | Prints the pair's current state: builder identity, review state for the plan, mailbox activity, bridge liveness. Read-only, safe whether or not a builder is paired |
| `tandem-handoff [plan]`  | Sends the builder a launch prompt naming a plan. Defaults to the most recently reviewed plan, else the newest plan in the project's plans directory. Refuses with exit 24 until the plan has cleared review. `--dry-run` sends nothing; `--context-limit N` sets a durable run's per-task prompt limit |
| `tandem-review [plan]`   | Asks the reviewer pane for a review of a plan without changing anything. The builder never reviews: with no reviewer recorded it sends nothing and says to declare a `reviewer` block or run `tandem-pair --reviewer KIND`. Same plan resolution as `tandem-handoff` |
| `tandem-verdict`         | Returns a review to the planner with a verdict. Run by the reviewer pane at the end of a review, and refused from any other pane (see [Which pane is the reviewer](reviews.md#which-pane-is-the-reviewer)). `tandem-review` writes `--role NAME`, the reviewer's recorded name, into the command, because Codex runs commands without the pane's `HERDR_ROLE` |
| `tandem-ask <agent> "q"` | Asks the paired agent a question and prints the reply. Works from either side                                                  |
| `tandem-relaylog`        | Prints the durable Q&A history every completed `tandem-ask` exchange leaves behind. `--limit N`, `--json`. Survives `tandem-unpair` |
| `tandem-handback`        | Hands finished work back to the planner with a report. Run by the builder when it is done                                      |
| `tandem-unpair`          | Closes the builder's pane and the reviewer pane `tandem-pair` recorded in `reviewer.json` (refusing while the reviewer is mid-review), stops the bridge, removes the mailbox, both of them when a pair made from a subdirectory has two. Refuses until every handback is signed off and the check passes. `--keep-relay` keeps them; `--clean-trust` removes the trust entries this harness added, for the builder's kind and the reviewer's |
| `tandem-bridge`          | Delivers questions, handbacks and review verdicts from the sandboxed side. Started by `tandem-pair`                             |
| `tandem-project`         | Prints resolved project settings as JSON                                                                                       |
| `tandem-init`            | Writes `.tandem.json` and ignores the relay mailbox and the Q&A history directory                                              |

`tandem-pair` also takes `--builder KIND`, `--builder-model`, `--builder-effort`,
`--builder-autonomy` for a single pairing, and `--approve-kind KIND`, which can be repeated to
approve the builder's and the reviewer's kinds in one run. `--reviewer KIND`,
`--reviewer-model` and `--reviewer-effort` override the `reviewer` block in `.tandem.json`
for a single pairing. A reviewer is required. Every plan review and every task review, on
both build engines, goes to the reviewer pane, so the builder can run a cheap model and the
review a strong one. With no `reviewer` block and no `--reviewer`, `tandem-pair` still pairs
and prints one warning, and review and build refuse until a reviewer is declared. See
[Choosing an agent](configuration.md#choosing-an-agent) and `guide/architecture/review-handback.md`.

The binary also has subcommands that the scripts call. `agent watch` and the bridge read
Herdr's event stream on its socket. `build wait` reads the run's ledger, and `pane wait-shell`
reads the pane's process info:

| Subcommand | Does |
| ---------- | ---- |
| `tandem agent watch [NAME...] [--root DIR] [--mailbox DIR] --timeout MS` | Waits for one of the named agents to change status. Prints `changed<TAB>NAME<TAB>STATUS` and exits 0, or prints `timeout` and exits 0. Exits 27 with `agent_exited<TAB>NAME` when an agent is gone, and 3 when the stream breaks or Herdr is unreachable. `--root` adds the durable run's builder and reviewer. `--mailbox` also returns on a message file in the relay's queues (`queued<TAB>PATH`), a change of its run marker (`run<TAB>PATH`), or a write to the run's state (`nudge<TAB>PATH`), and exits 1 when the file watcher cannot start |
| `tandem agent self [--expect NAME [--claimed ROLE] [--timeout MS]]` | Prints the name of the agent in this pane (`HERDR_PANE_ID`), or nothing. With `--expect`, prints whether this shell is the reviewer pane NAME instead: `accept` or `refuse`, a reason and a detail, separated by tabs. See [Which pane is the reviewer](reviews.md#which-pane-is-the-reviewer) |
| `tandem agent prompt NAME TEXT --as KIND [--wait-unblocked MS]` | Sends the prompt the way KIND is delivered and confirms the turn ran. KIND is task, review, handback, verdict, question or notice, and each kind's checks are one row in `cmd/tandem/deliver.go`. The planner's kinds wait up to 2 minutes for a blocked agent, or for a pane showing a subagent or a list, before typing. `--wait-unblocked` sets that wait in MS and needs `--as`. Every `tandem agent prompt` refuses such a pane with `agent_input_elsewhere` (see [When tandem refuses](refusals.md#a-pane-showing-a-subagent-or-a-list)) |
| `tandem build wait --root DIR (--until STATE)... \| --until-not STATE --timeout MS [--tick]` | Prints the run's status and exits 0 once it reaches a wanted state, or 23 at the timeout. `sent` matches once the current attempt's prompt was dispatched. `--tick` runs a tick before each check |
| `tandem build tick --root DIR --observe-agents` | After the tick, pauses a run whose builder or reviewer Herdr reports gone, with code `agent_exited`. `tandem build recover` resumes it once the agent is back |
| `tandem pane wait-shell PANE --timeout MS` | Waits until the pane's foreground process is its shell. Exits 23 at the timeout and 3 when Herdr is unreachable |

Between passes the bridge waits on `tandem agent watch --mailbox`, plus `--root` while a
durable run is active. A command that changes rows in the run's state leaves a `nudge`
file in the state root when it closes the store, so a planner command such as `tandem build stop` or `review-record` wakes the
bridge at once. `HERDR_BRIDGE_BACKSTOP_SECONDS` (default 30) bounds a missed file event.
`HERDR_BRIDGE_POLL_SECONDS` (default 1) paces a pass after a failed watch. The bridge
writes `$MAILBOX/bridge.ready` once it is running, and `tandem-pair` waits for that file.

Keybindings: `prefix+i` pair, `prefix+shift+h` handoff, `prefix+a` toggle agents. These
come from `config.toml`, so you only have them if you took the example config or copied
those entries across — `install.sh` never overwrites an existing one. The commands work
without them.

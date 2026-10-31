# tandem

tandem runs two coding agents side by side in [Herdr](https://herdr.dev). Claude Code plans,
and the builder can be any supported agent CLI: `codex`, `claude`, `copilot` or `opencode`.
Each repository declares the builder's model, effort and autonomy. A third pane, the
reviewer, checks every plan and every task on its own model, so the builder never reviews
its own work.

Either agent can ask the other a question, and nobody has to copy the answer across. Both
agents work in the same checkout on one branch, so there are no worktrees to merge back.

tandem works in any repository. It detects what it needs, such as the plans folder and the
check command, or reads it from that repo's `.tandem.json`.

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/emsnoel/tandem/main/install.sh | bash
```

This installs the latest release into `~/.local/bin`. You need `curl`, `tar`, `jq` and
`shasum` (or `sha256sum`). You don't need Go. Run the same line again to upgrade. The
installer never overwrites an existing Herdr config.

You also need Herdr 0.9.3 or later, and Herdr's integration for each agent you use:

```bash
herdr integration install claude   # always needed, because the planner is Claude Code
herdr integration install codex    # repeat for each builder or reviewer kind you use
```

[guide/install.md](guide/install.md) covers installing a set version, building from source,
uninstalling and the full list of requirements.

## Quickstart

Open a Claude Code pane inside Herdr, in the repo you want to work on, and run:

```bash
tandem-init     # writes .tandem.json with the plans folder, check command and conventions file
```

Add a reviewer to `.tandem.json`. Reviews and builds refuse to start without one:

```json
{ "reviewer": { "kind": "codex" } }
```

Start the pair, write a plan into the plans folder, and hand it over:

```bash
tandem doctor     # lists anything that would stop the pair, with the fix for each
tandem-pair       # names this pane "planner" and starts the builder and reviewer panes
tandem-review     # the reviewer checks the newest plan
tandem-handoff    # once the review passes, the builder starts task 1
```

The builder works on one task at a time, each in a fresh conversation. tandem sends the
next task when the last one passes its check. Either agent can ask the other a question:

```bash
tandem-ask builder "how far through the plan are you?"
```

When the build ends, the planner gets one report. Read it, then close the pair:

```bash
tandem-unpair --verified <id>    # the report message gives the id
```

`tandem-unpair` refuses until you sign off the work and the project's check passes. Every
refusal prints the command that fixes it. [guide/walkthrough.md](guide/walkthrough.md)
goes through the same steps in detail.

## Commands

| Command | Does |
| ------- | ---- |
| `tandem-init` | Writes `.tandem.json` and adds tandem's local folders to `.gitignore` |
| `tandem-pair` | Starts the builder, the reviewer and the relay bridge, or reuses running ones. Safe to run again |
| `tandem-status` | Prints the pair's state. Changes nothing |
| `tandem-review` | Asks the reviewer to review a plan |
| `tandem-handoff` | Sends a reviewed plan to the builder |
| `tandem-ask` | Asks the other agent a question and prints the reply |
| `tandem-unpair` | Runs the check, then closes the builder and reviewer panes |

`tandem pair` and `tandem-pair` do the same thing. Run `tandem` on its own to list every
command. [guide/commands.md](guide/commands.md) describes all twelve commands and their flags.

## Guides

Using tandem:

- [Install](guide/install.md): versions, building from source, uninstalling and requirements.
- [A worked example](guide/walkthrough.md): one feature, from setup to closing the pair.
- [Configuration](guide/configuration.md): `.tandem.json`, choosing each agent, and your user config.
- [Commands](guide/commands.md): every command, and the subcommands of the `tandem` binary.
- [When tandem refuses](guide/refusals.md): each exit code and how to fix it.
- [Plans and reviews](guide/reviews.md): how to write a plan, and when a review still counts.
- [Builds](guide/builds.md): how tandem runs a plan task by task, and what stops a build.
- [Launching the builder](guide/launching.md): the trust dialogs and flags tandem handles for each agent.
- [Optional Jev features](guide/jev.md): staffing advice and review triage.
- [Durable state](guide/durable-state.md): the `tandem state` commands.

How it works:

- [How the relay works](guide/relay.md): how a sandboxed builder sends messages, and the safety rules.
- [Design notes](guide/design.md): why tandem is built the way it is.
- [Build engines](guide/build-engines.md), [durable runner operations](guide/durable-runner-operations.md)
  and the [durable runner rollout report](guide/durable-runner-rollout-report.md).

Working on tandem:

- [Working on tandem](guide/contributing.md): the tests and the code layout.
- [Versioning](guide/versioning.md) and [releasing](guide/releasing.md).

## Licence

tandem uses the MIT licence. See [LICENSE](LICENSE).

The `tandem` binary includes Go modules under the MIT and BSD-3-Clause licences, and
SQLite, which is public domain. Each release tarball includes their notices in
`THIRD_PARTY_NOTICES`, which the packaging script generates from the binary.

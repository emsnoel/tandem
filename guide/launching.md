# Launching the builder unattended

Nobody watches the builder, so any interactive prompt stalls it. The pane reports `idle`,
the prompt swallows the launch message, and the builder waits forever. So before the pane
starts work, `tandem-pair` handles each prompt the builder's kind is known to show. If it
can't clear one, it refuses to launch.

| Kind | Prompt | What tandem does |
| ---- | ------ | ---------------- |
| codex | "Do you trust the files in this folder?" | Appends a marked `[projects."<cwd>"]` entry to `~/.codex/config.toml` |
| codex | Approval dialogs for each command | Passes `--ask-for-approval never`, from `autonomy` |
| codex | "Update available", shown when a newer Codex release exists | Passes `-c check_for_update_on_startup=false`, for the builder and reviewer only |
| codex | The "Hooks need review" screen, shown when Codex has hooks it hasn't accepted. Herdr reports it as idle | Reads the pane after launch and refuses to call the pair ready (see [Reading the pane](#reading-the-pane)) |
| claude | A trust dialog not yet accepted | Reports it and writes nothing. Claude Code owns and rewrites `~/.claude.json`, so tandem never touches it. Pairing refuses with exit 23. Run `claude` once in the folder, accept the dialog, and pair again |
| claude | The Bypass Permissions warning, shown at `full` autonomy until a person accepts it once | Reads the pane after launch, names the warning and refuses with exit 23. Accept it in the pane and pair again |
| copilot | "Do you trust the files in this folder?" | Adds a `trustedFolders` entry to `$COPILOT_HOME/config.json`, but only when you chose the copilot builder yourself. If the repository asked for it and you haven't approved that on this machine, tandem reports it instead, and pairing refuses with exit 23. Approve it once with `tandem-pair --approve-kind copilot`. tandem keeps the `//` comment banner and every other key. `tandem-unpair --clean-trust` removes the entry, but only if tandem added it |
| copilot | The session-restore picker, which Herdr reports as idle | Passes `--session-id <fresh uuid>`, for the builder and reviewer only |
| copilot | Approval dialogs for commands, paths and URLs | Passes `--allow-all-tools --allow-all-urls`, from `autonomy`, plus `--allow-all-paths` for the builder and reviewer only |
| copilot | The `ask_user` tool, which asks a person the builder doesn't have | Passes `--no-ask-user`, for the builder and reviewer only |
| agy | The "Do you trust the contents of this project?" folder-trust prompt | Adds the folder to `trustedWorkspaces` in agy's settings, but only when you chose the agy builder yourself. Otherwise tandem reports it, and pairing refuses with exit 23. Approve it once with `tandem-pair --approve-kind agy`. `tandem-unpair --clean-trust` removes the entry, but only if tandem added it |
| agy | Permission prompts and dialogs | Reads the pane (see [Reading the pane](#reading-the-pane)). A footer line that starts `↑/↓ Navigate` marks a dialog. `esc to cancel` alone doesn't, because agy shows it while it works |

**Start an agy planner in default mode.** Run `agy --settings '{"agentMode":""}'`, or switch
to it with shift+tab. agy starts in Auto, and tandem's commands can't reach Herdr from
Auto's sandbox. **An agy builder or reviewer in Auto never stops on tandem's own commands.**
tandem pre-approves `tandem-handback`, `tandem-verdict` and `tandem-ask` with agy allow
rules, because Auto's review once held a reviewer's `tandem-verdict` at "Run this
command?". The review can still ask about other commands, and tandem's stall pause names
that prompt. Set the autonomy to `full` for a pane that must never ask.

**A prompt that needs a person means different things for each role.** For the planner,
tandem only notes it, because someone is sitting at that pane. For a builder or a reviewer,
it refuses with exit 23. Launching into an open dialog would cause exactly the silent stall
tandem exists to prevent. The reviewer also runs unattended, so it gets every check and flag
in this table for its own kind.

`tandem-pair --dry-run` reports what each check would do and writes nothing, so you can run
it safely on a real machine.

## Reading the pane

No list of known prompts stays complete. A CLI can add a startup screen in any release, and
Herdr reports some of them as `idle`. So `tandem-pair` also looks at what each pane shows.
Once the builder and the reviewer are started, it runs `tandem agent ready` for each one,
before it says `pair ready`.

`tandem agent ready NAME` reads the pane's screen. It exits 23, and prints the screen, when
the pane shows a dialog that a person has to answer:

- a screen tandem recognises as holding the keys (`internal/agent/screen.go` lists them);
- for Codex, any screen whose last line offers Esc, such as `enter confirm · esc skip`.
  Every Codex dialog measured so far ends that way, and its main input never does;
- for Claude Code, any screen whose last line offers `Esc to cancel`, and for Copilot,
  any screen whose footer offers `esc to cancel`. Their trust prompts and Claude Code's
  Bypass Permissions warning end that way, and neither main input does;
- for agy, any screen whose bottom lines offer `↑/↓ Navigate`. `esc to cancel` alone doesn't
  count, because agy shows it while it works, and the marker only counts at the bottom;
- anything Herdr itself reports as `blocked`.

`tandem-pair` then stops with exit 23 and names every pane that needs you:

```
tandem-pair: the reviewer is not at its prompt: reviewer's pane shows Codex's "Hooks need review" prompt, which waits for an answer.

      Hooks need review
      6 hooks are new or changed.
      ...

  Answer it in pane w8:p9Z.

tandem-pair: the pair is not ready. Run tandem-pair again once each pane is at its prompt.
```

The panes stay open. Answer each one, run `tandem-pair` again, and it adopts them.

A screen that nothing recognises passes. A wrong refusal would stop every pairing after a
CLI redesign, and a prompt that never starts a turn is still caught when it is sent (exit
25). `HERDR_SKIP_HOOK_CHECK=1` pairs anyway.

The same reading guards every prompt tandem sends later. `tandem agent prompt` types
nothing into a pane that shows one of these screens.

## Codex hooks

Which hooks run in a Codex pane is your decision, and you make it in Codex. Run `/hooks`
there and accept the ones you want. Codex remembers the answer, so every Codex pane tandem
starts afterwards runs those hooks. tandem never accepts a hook for you.

codex-cli 0.167.0 opens on a "Hooks need review" screen when it has hooks it hasn't
accepted. The screen offers to review them, trust them all, or continue without trusting
them. Herdr reports a pane on that screen as `idle`. `tandem-pair` reads the pane, finds
the screen and stops, as above. Choosing "Continue without trusting" clears it for that
session only, and Codex then prints `Hook failed` after each step.

tandem used to guess from Codex's files whether Codex would ask. The guess was wrong both
ways (F29 and F31 in `guide/design.md`), so it is gone, and with it the
`.tandem-hook-baseline` file it kept in `$CODEX_HOME`. You can delete that file.

To run hooks without accepting them, for automation, put Codex's own flag in your user
config, where a repository can't reach it:

```json
{ "adapters": { "codex": { "args": ["--dangerously-bypass-hook-trust"] } } }
```

With that flag, every hook source Codex loads runs unchecked, for that run of Codex.

## Sandboxes

**tandem leaves the sandbox on for a Codex builder, on purpose.** `autonomy: auto` maps to
`--ask-for-approval never` and a permission profile tandem names `tandem`. Turning approvals
off doesn't turn the sandbox off. The builder still can't write outside its workspace, write
`.git`, or reach the internet. It gets an error instead of a dialog:

```
zsh:1: operation not permitted: /Users/you/.some-file
```

The model reads the error and works around it.

The profile is passed as `-c` settings on Codex's command line, so `~/.codex/config.toml`
needs no entry. It extends Codex's own `:workspace` profile (`:read-only` for
`autonomy: readonly`) and adds three things:

- **Herdr's socket.** tandem's commands reach Herdr from the builder's and reviewer's shells,
  so a handback, a question or a verdict goes to the planner directly. The mailbox stays as
  the fallback when the socket can't be reached (see [How the relay works](relay.md)).
- **Unix sockets inside the checkout**, such as a test's fake server.
- **Loopback.** A command may bind, listen on and connect to `127.0.0.1` and `::1`, over TCP
  or UDP, such as a test server.

The internet stays shut. Codex opens Unix sockets only when its profile turns the network
on, so the profile also turns on Codex's `network_proxy` feature with no domain allowed. A
direct connection out fails with `Operation not permitted`, and a command that names a host
on its command line is refused before it runs.

Anything that reaches Herdr's socket can open a pane and run commands there, outside the
sandbox. So the socket gives the builder and the reviewer that power too. You chose this
trade when you pair a Codex builder: it saves the mailbox detour and lets a sandboxed agent
run every tandem command. `autonomy: full` drops the sandbox altogether, as before.

### Letting a check run in the sandbox

Some build tools need more than the profile gives. Gradle is one. Its wrapper locks a file
under `~/.gradle`, which is outside the workspace, and Gradle's own listener is refused behind
Codex's network proxy even though loopback is open. So `./gradlew check` fails with
`Operation not permitted`. A reviewer then reports its check probe as "could not verify".

You can widen the sandbox for one agent kind in your user config,
`~/.config/herdr/tandem.json`:

```json
{ "adapters": { "codex": { "sandbox": { "writable_dirs": ["~/.gradle"], "network": true } } } }
```

- `writable_dirs` lists directories the agent may write besides its checkout. A leading
  `~/` means your home directory. tandem drops a relative entry.
- `network` turns Codex's network proxy off, which opens the internet too. Codex has no
  setting in between. On codex-cli 0.169.0 a Gradle check needs both grants: measured with
  Gradle 9.2.0, `./gradlew help --offline` fails without `network` and passes with both.

The grant applies to the builder and the reviewer of that kind. A Codex agent gets one
`filesystem` entry per directory in its profile, and a network grant turns the proxy off. A
`readonly` agent gets neither, because its sandbox writes nothing. A Claude or Copilot agent
runs without tandem's sandbox and needs no grant.

A repository can't set `sandbox`, because it grants authority. tandem ignores it in
`.tandem.json` and prints a warning. A running agent keeps the flags it started with, so
restart its pane after you change the grant.

`tandem doctor` warns before a pairing when the repository uses Gradle and a sandboxed
builder or reviewer lacks the grant. It also warns when the granted Gradle home doesn't
exist yet, since the sandbox can't create it. It looks for a `gradlew` or `*.gradle` file at
the root, or `gradle` in `check_command`, and never runs the check. `GRADLE_USER_HOME` moves
the directory it expects.

You can override autonomy for a single session, but the defaults are the supported setup:

```bash
tandem-pair --builder-autonomy full          # preferred
HERDR_BUILDER_AUTONOMY=full tandem-pair      # the same, through the environment
```

`HERDR_BUILDER_SANDBOX` and `HERDR_BUILDER_APPROVAL` still work for a Codex builder.
tandem translates them to the nearest autonomy value and prints a deprecation warning.

You can run `tandem-pair` again at any time. It adopts an agent that is already running
instead of starting a second one. After a server restart, running it again gives the
agents their names back.

## Adoption

When one unnamed agent is already running in the planner's tab, `tandem-pair` adopts it
instead of starting another. It never adopts agents in other tabs, and with `--builder KIND`
it adopts only an agent of that kind.

An adopted agent keeps the settings it was started with. tandem reports the model, effort
and autonomy from `.tandem.json` as not enforced, and doesn't apply them after the fact.
Restart that pane when the configured settings must take effect.

tandem adopts a reviewer only when an agent already has this checkout's reviewer name
(see [Several sessions on one repository](#several-sessions-on-one-repository)). It keeps
the settings it was started with, and tandem reports a declared model or effort it doesn't
run with as not enforced. Close it and pair again to apply them.

## Several sessions on one repository

One checkout holds one pair. To run a second pair on the same repository, give it its own
git worktree:

```bash
git worktree add ../repo-feat-x
cd ../repo-feat-x
```

Start a planner in that folder and run `tandem-pair`. The worktree gets its own relay, its
own bridge and its own agents, so the two pairs never read each other's messages.

### Names

`tandem-pair` picks the agent names from the checkout it runs in:

- The main checkout keeps `planner`, `builder` and `reviewer`.
- A linked worktree gets `planner-<folder>`, `builder-<folder>` and `reviewer-<folder>`. The
  folder part is the worktree's folder name in lowercase. It keeps `a` to `z`, `0` to `9`,
  `_` and `-`, turns every other character into `-`, collapses repeated `-` and trims `-`
  from both ends. Then it cuts the result to 23 characters and trims a `-` the cut leaves at
  the end. The folder `Repo Feat_X` gives `planner-repo-feat_x`.
- When two linked worktree folders give the same name, or the name is empty, the suffix is
  the first 6 characters of the checkout id instead. The main checkout's folder never
  clashes, because the main checkout keeps the plain names.
- `HERDR_PLANNER_NAME`, `HERDR_BUILDER_NAME` and `HERDR_REVIEWER_NAME` override a name.

Each pane's label shows the role, then what the name adds to it, then the agent's kind.
The builder of the worktree `feat-x` reads `builder · feat-x (codex)`. The main checkout's
panes keep `builder (codex)`. A name set by hand shows whole, as `builder · bob (codex)`.
`tandem-pair` sets the label with `herdr pane rename`. Herdr shows it on the pane's border
and in the window title. The sidebar shows it only where a row in `config.toml` names the
`pane` token, and the example config names none.

### What each pane is doing

Each pane's `activity` token says what the pane works on. The example config shows it as the
second sidebar row of each agent, under the role. The token never changes the pane's name,
so a name you set by hand stays.

| Pane | Shows | Set by | Cleared when |
| --- | --- | --- | --- |
| builder | `task 2/3 · count plan` | the bridge, while a run is running or paused | the run finishes, stops or is archived |
| planner | `waiting for handback`, or `run paused` | the bridge, while a run is live | the run ends, or `tandem-unpair` runs |
| reviewer | `reviewing count plan` | `tandem-review`, once the reviewer has the prompt | the verdict is stored or delivered |

The plan's name is its file name without the leading date and the `.md`, with each `-` as a
space, so `2026-11-17-count-plan.md` gives `count plan`. The whole text is cut to 30
characters, which fits the sidebar at its widest (36 columns) beside the state icon.

Updates are best-effort. A Herdr call that fails never fails a delivery, a tick or a review.
The bridge keeps the last text it wrote for each agent in `.tandem-relay/activity/`, and
calls Herdr only when a text changes. When the socket is unreachable, it writes nothing,
and the next pass tries again.

An existing Herdr config needs the row added by hand, because the installer never
overwrites one. The installer names the `$activity` row when it is missing. Copy the
`rows_by_agent` lines from `config/config.toml.example`.

`tandem-pair` records the names in `.tandem-relay/pair.json`, and every script reads them
from that file. `tandem paths names [--root DIR]` prints them for a checkout. It writes
nothing unless `tandem-pair` passes `--bind`, because the builder and the reviewer run it
from a sandbox that cannot write the state home. The first line of `tandem-status` shows them, as
`pair     P, B, R  (checkout <path>)`. `tandem doctor` shows them as "this checkout pairs as
..." and warns when an agent in another checkout holds one of them.

A checkout id comes from the state registry, and only a real `tandem-pair` run writes one.
Until then, `tandem-pair --dry-run`, `tandem-status` and `tandem doctor` show `<id>` in its
place, and the other scripts stop and ask you to run `tandem-pair` in that checkout.

### Setup for each new folder

Each worktree folder is new to the agent CLIs, so the first `tandem-pair` there can stop on:

- Claude Code's trust dialog. Run `claude` once in the folder and accept it. tandem will not
  write `~/.claude.json`.
- A builder kind that `.tandem.json` requests and you haven't approved on this machine.
  Doctor says `builder kind codex requested by .tandem.json, not approved`. Run
  `tandem-pair --approve-kind codex`, with the kind it names.
- Codex's trust prompt. `tandem-pair` writes the `~/.codex/config.toml` entry for the folder.
- Copilot's trusted folders. `tandem-pair` adds the folder only for a builder kind you approved.

Run `tandem doctor` in the new folder first to see which of these apply.

### Closing a pair

`tandem-unpair` closes only the pair of the worktree it runs in. The other worktrees keep
their agents and bridges.

If a worktree's planner pane is gone while its bridge still runs, `tandem-pair` from a new
pane refuses and prints the command that clears it:

```
HERDR_PLANNER_NAME=<owner> tandem-unpair --force
```

### Agents from another checkout

`tandem-pair` refuses, with exit 35, to adopt a builder or reviewer whose pane works in
another checkout. That agent sends its verdicts and handbacks to its own checkout's relay,
so adopting it would cross two pairs. The refusal names the pane and the checkout:

```
tandem-pair: the agent named 'builder' in pane w8:p9Z works in another checkout: /path/to/other.
tandem-pair: tandem does not adopt it, because its messages would go to that checkout's pair.
tandem-pair: close it from that checkout with tandem-unpair, or pair here under other names (HERDR_BUILDER_NAME, HERDR_REVIEWER_NAME).
```

When this checkout is in git, a pane outside git counts as another checkout.

### After upgrading

Older versions named every pair `planner`, `builder` and `reviewer`, and kept the planner's
name in `.tandem-relay/planner-name`. The scripts now read the names from `pair.json`. A
pair that was live before the upgrade has no such file, so check where it runs:

- In a main checkout, the names stay the same, so nothing is needed. If the pair runs
  under custom names, run `tandem-pair` once with the same `HERDR_PLANNER_NAME`,
  `HERDR_BUILDER_NAME` and `HERDR_REVIEWER_NAME` values to record them.
- In a linked worktree, the pair runs under the plain names, but the worktree now derives
  `planner-<folder>`. Run this once in its planner pane to record the names it runs under:

  ```bash
  HERDR_PLANNER_NAME=planner HERDR_BUILDER_NAME=builder HERDR_REVIEWER_NAME=reviewer tandem-pair
  ```

  Or close the pair with `tandem-unpair`, with those three variables set, and pair again.

  If you pair the worktree under its new names without this step, `tandem-pair` moves the
  bridge to the new planner name and starts a new builder and reviewer. The old builder and
  reviewer keep running under the plain names until you close their panes.

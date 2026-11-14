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
`--sandbox workspace-write --ask-for-approval never`. Turning approvals off doesn't turn the
sandbox off. The builder still can't write outside its workspace or reach the network. It
gets an error instead of a dialog:

```
zsh:1: operation not permitted: /Users/you/.some-file
```

The model reads the error and works around it. This matters, because the relay exists only
because the builder is sandboxed (see [How the relay works](relay.md)). Widening `--sandbox`
to `danger-full-access` would let Codex reach the Herdr socket directly and make the mailbox
unnecessary. `guide/design.md` explains why that is a bad trade for an agent running plans
on its own.

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

tandem adopts a reviewer only when an agent already has the reviewer's name: `reviewer`,
or the value of `HERDR_REVIEWER_NAME`. It keeps the settings it was started with, and tandem
reports a declared model or effort it doesn't run with as not enforced. Close it and pair
again to apply them.

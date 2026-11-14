# Launching the builder unattended

Nobody watches the builder, so any interactive prompt stalls it. The pane reports `idle`,
the prompt swallows the launch message, and the builder waits forever. So before the pane
starts work, `tandem-pair` handles each prompt the builder's kind is known to show. If it
can't clear one, it refuses to launch.

| Kind | Prompt | What tandem does |
| ---- | ------ | ---------------- |
| codex | "Do you trust the files in this folder?" | Appends a marked `[projects."<cwd>"]` entry to `~/.codex/config.toml` |
| codex | Approval dialogs for each command | Passes `--ask-for-approval never`, from `autonomy` |
| codex | The "Hooks need review" screen, shown when Codex has hook sources it hasn't accepted. Herdr reports it as idle | Reports it and writes nothing. Pairing refuses with exit 23. Accept the hooks in Codex with `/hooks`, and pair again (see [Codex hooks](#codex-hooks)) |
| claude | A trust dialog not yet accepted | Reports it and writes nothing. Claude Code owns and rewrites `~/.claude.json`, so tandem never touches it. Pairing refuses with exit 23. Run `claude` once in the folder, accept the dialog, and pair again |
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

## Codex hooks

Before it starts a Codex builder, `tandem-pair` also checks whether Codex has hook sources
it hasn't accepted. Two examples are a newly enabled plugin that ships hooks, and a hook
whose bytes changed since the last pairing. tandem doesn't try to recompute Codex's
`trusted_hash`. Codex computes that value over an internal format, and a copy of that code
would break without warning on a Codex update.

codex-cli 0.167.0 opens on a "Hooks need review" screen when it finds such a source. The
screen offers to review the hooks, trust them all, or continue without trusting them. Herdr
reports a pane on that screen as `idle`, so `tandem-pair` would say `pair ready` and the
first prompt sent to the pane would be typed into the screen and lost. We measured this on
2026-11-14 (F29 in `guide/design.md`). So the check reports `manual`, and pairing refuses a
builder or a reviewer with exit 23. For a planner tandem only notes it, because someone is
sitting at that pane.

Tests against codex-cli 0.151.0 and 0.152.1 showed that those versions skip an unaccepted
hook without asking. Nobody has measured the versions between them and 0.167.0. tandem
refuses on every version, because a refusal that turns out unnecessary costs one command and
a pane stuck on a screen costs the run. The refusal prints the installed `codex --version`
next to the measured versions.

To clear the refusal, accept the sources in Codex with `/hooks`, then pair again. Choosing
"Continue without trusting" on the screen clears it for that session only, and Codex then
prints `Hook failed` after each step. To run the hooks without accepting them, for
automation, put Codex's own flag in your user config, where a repository can't reach it:

```json
{ "adapters": { "codex": { "args": ["--dangerously-bypass-hook-trust"] } } }
```

With that flag, every hook source Codex loads runs unchecked, for that run of Codex.

```bash
HERDR_SKIP_HOOK_CHECK=1 tandem-pair   # ignore a refusing check and pair anyway
```

With that variable set, tandem launches into the screen and you clear it in each pane
yourself. It does not make the hooks run.

If the check can't read the config or doesn't recognise its layout, it says nothing and
doesn't block pairing.

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

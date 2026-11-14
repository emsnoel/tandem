# Launching the builder unattended

Nobody watches the builder, so any interactive prompt stalls it. The pane reports `idle`,
the prompt swallows the launch message, and the builder waits forever. So before the pane
starts work, `tandem-pair` handles each prompt the builder's kind is known to show. If it
can't clear one, it refuses to launch.

| Kind | Prompt | What tandem does |
| ---- | ------ | ---------------- |
| codex | "Do you trust the files in this folder?" | Appends a marked `[projects."<cwd>"]` entry to `~/.codex/config.toml` |
| codex | Approval dialogs for each command | Passes `--ask-for-approval never`, from `autonomy` |
| codex | The "Hooks need review" screen, shown when Codex has hooks it hasn't accepted. Herdr reports it as idle | Warns before launch when Codex's files suggest it, and never refuses on that guess (see [Codex hooks](#codex-hooks)) |
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

Which hooks run in a Codex pane is your decision, and you make it in Codex. Run `/hooks`
there and accept the ones you want. Codex remembers the answer, so every Codex pane tandem
starts afterwards runs those hooks. tandem never accepts a hook for you.

codex-cli 0.167.0 opens on a "Hooks need review" screen when it has hooks it hasn't
accepted. The screen offers to review them, trust them all, or continue without trusting
them. Herdr reports a pane on that screen as `idle`, so a prompt sent to it is typed into
the screen and lost (F29 in `guide/design.md`). Versions 0.151.0 and 0.152.1 skipped such
hooks without asking.

Before it starts a Codex pane, `tandem-pair` reads Codex's files and warns when a hook
source looks unaccepted. That is a guess about what Codex will do, and it never stops a
pairing. It has been wrong both ways: it missed hooks that 0.167.0 asked about, and it
named a plugin that Codex loads no hooks from (F31). tandem doesn't try to recompute Codex's
`trusted_hash` either. Codex computes that value over an internal format, and a copy of
that code would break without warning on a Codex update.

The check looks where Codex looks. For each enabled plugin it reads the `hooks` entry of
`.codex-plugin/plugin.json`:

| The manifest says | The hooks are in |
| --- | --- |
| nothing, or there is no manifest | `hooks/hooks.json` |
| a path, such as `"./hooks/hooks.codex.json"` | that file |
| an object, such as `{"hooks": {...}}` | the manifest itself |
| an empty object, `{}` | nowhere: the plugin has no Codex hooks |

A file with no hook in it is skipped, because Codex has nothing to accept there.

To clear the warning, accept the hooks in Codex with `/hooks`. Choosing "Continue without
trusting" on the screen clears it for that session only, and Codex then prints `Hook failed`
after each step. To run the hooks without accepting them, for automation, put Codex's own
flag in your user config, where a repository can't reach it:

```json
{ "adapters": { "codex": { "args": ["--dangerously-bypass-hook-trust"] } } }
```

With that flag, every hook source Codex loads runs unchecked, for that run of Codex.

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

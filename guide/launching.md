# Launching the builder unattended

Nobody watches the builder, so any interactive prompt stalls it. The pane reports `idle`,
the prompt swallows the launch message, and the builder waits forever. So before the pane
starts work, `tandem-pair` handles each prompt the builder's kind is known to show. If it
can't clear one, it refuses to launch.

| Kind | Prompt | What tandem does |
| ---- | ------ | ---------------- |
| codex | "Do you trust the files in this folder?" | Appends a marked `[projects."<cwd>"]` entry to `~/.codex/config.toml` |
| codex | Approval dialogs for each command | Passes `--ask-for-approval never`, from `autonomy` |
| codex | Hooks that Codex hasn't accepted | Warns before launch, and never refuses. A skipped hook weakens the builder but doesn't stall it. `HERDR_STRICT_HOOK_CHECK=1` turns the warning into a refusal (see [Codex hooks](#codex-hooks)) |
| claude | A trust dialog not yet accepted | Reports it and writes nothing. Claude Code owns and rewrites `~/.claude.json`, so tandem never touches it. Pairing refuses with exit 23. Run `claude` once in the folder, accept the dialog, and pair again |
| opencode | None | Nothing |
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

Tests against codex-cli 0.151.0 and 0.152.1 showed that Codex skips an unaccepted hook
without asking. So the check reports `warning` and pairing continues. Only
`HERDR_STRICT_HOOK_CHECK=1` turns it into the exit 23 refusal the other checks give. A
refusal is for a dialog that would stall the pane, and a skipped hook stalls nothing.

The warning still matters. From the outside, a skipped hook looks the same as a working
one, so the builder may be running without the plugin behaviour you expect. Older Codex
versions did ask (see F12), in a dialog Herdr can't see.

Only those two versions have been tested, and both behave the same way. No tested version
still asks, so there is no version boundary to check an installed CLI against. Instead, the
warning prints the installed `codex --version` next to the tested versions, so you can
judge whether yours is close enough.

```bash
HERDR_STRICT_HOOK_CHECK=1 tandem-pair   # refuse to pair instead of warning
HERDR_SKIP_HOOK_CHECK=1   tandem-pair   # ignore a refusing check and pair anyway
```

Neither variable makes the hooks run. To clear the warning, accept the sources in Codex
with `/hooks`. To run them without accepting them, for automation, put Codex's own flag in
your user config, where a repository can't reach it:

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

**opencode has no sandbox at all.** Its `--auto` is an on/off switch for approvals, not a
permission level, so an opencode builder is always unconfined. It can reach the Herdr
socket directly, which is why it needs no relay. Choose it knowing that a Codex builder's
`workspace-write` protection doesn't exist there.

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

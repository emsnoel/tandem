# How the builder is launched unattended

The builder has no human watching it, so **any** interactive gate is a stall — a pane that
reports `idle`, swallows its launch prompt and waits forever. `tandem-pair` therefore runs
the gates for the builder's kind before the pane starts work, and refuses to launch rather
than walk into one it cannot clear.

| Kind | Gate | Handling |
| ---- | ---- | -------- |
| codex | "Do you trust the files in this folder?" | a marked `[projects."<cwd>"]` entry appended to `~/.codex/config.toml` |
| codex | per-command approval dialogs | `--ask-for-approval never`, from `autonomy` |
| codex | hooks Codex has not accepted | **warned** before launch, never refused — a skipped hook degrades the builder, it does not stall it. `HERDR_STRICT_HOOK_CHECK=1` escalates it to a refusal; see below |
| claude | trust dialog not yet accepted | **reported, never written** — `~/.claude.json` is state Claude Code owns and rewrites, so tandem will not touch it. Pairing **refuses with exit 23**; run `claude` once in the directory, accept, and re-pair |
| opencode | — | none |
| copilot | "Do you trust the files in this folder?" | a `trustedFolders` entry added to `$COPILOT_HOME/config.json`, **only when you chose the copilot builder yourself**. If the *repository* asked for it and you have not approved that here, tandem reports instead of writing and pairing **refuses with exit 23** — approve once with `tandem-pair --approve-kind copilot`. The `//` comment banner and every other key are preserved; `tandem-unpair --clean-trust` removes the entry, and only if tandem added it |
| copilot | the session-restore picker, which Herdr reports as **idle** | `--session-id <fresh uuid>`, for the builder and reviewer roles only |
| copilot | per-command, path and URL approval dialogs | `--allow-all-tools --allow-all-urls`, from `autonomy`, plus `--allow-all-paths` for the builder and reviewer roles only |
| copilot | the `ask_user` tool, which asks a human the builder does not have | `--no-ask-user`, for the builder and reviewer roles only |

**A gate needing a human means different things per role.** For a planner it is a note:
someone is sitting there. For a builder or a reviewer it is a refusal (**exit 23**), because
launching into an uncleared dialog produces exactly the silent stall the harness exists to
prevent. The reviewer runs unattended too, so it gets every gate and flag in this table
under its own kind.

`tandem-pair --dry-run` reports what each gate *would* do and writes nothing, so it is safe
to run against a real machine.

`tandem-pair` also checks, before starting the builder, whether Codex has hook sources
it has not accepted — a newly enabled plugin that ships hooks, or a hook whose bytes
changed since the last pairing. It does not try to recompute Codex's `trusted_hash`;
that value is over an internal canonical form and any reimplementation would break
silently on a Codex update.

Measured against codex-cli 0.151.0 and re-measured against 0.152.1, an unaccepted hook
is **silently skipped, not prompted** — so the gate reports `warning`, pairing
continues, and only `HERDR_STRICT_HOOK_CHECK=1` turns it into the exit-23 refusal the
other gates give. That split is the whole point: a refusal is for a dialog that would
*stall* the pane, and a skipped hook stalls nothing. The warning still matters, because
a skipped hook is indistinguishable from a working one from the outside, so the builder
can be running without the plugin behaviour you assume it has. Older Codex versions did
prompt (see F12), and that dialog is invisible to Herdr, so:

Only those two versions have ever actually been measured, and both behave the same way —
there is no recorded version that still prompts, so there is no boundary to check an
installed CLI against. Rather than invent one, the warning itself names the installed
`codex --version` alongside what was measured, so you can judge for yourself whether your
version is close enough to trust.

```bash
HERDR_STRICT_HOOK_CHECK=1 tandem-pair   # refuse to pair instead of warning
HERDR_SKIP_HOOK_CHECK=1   tandem-pair   # ignore a refusing gate and pair anyway
```

Neither env var makes the hooks run. To clear the finding, accept the sources in Codex
with `/hooks`. To run them **without** accepting them — the automation case — put
Codex's own flag in your user config, where a repository cannot reach it:

```json
{ "adapters": { "codex": { "args": ["--dangerously-bypass-hook-trust"] } } }
```

It does what it says: every hook source Codex loads runs unvetted, for that invocation.

The check fails open: an unreadable config or an unrecognised layout says nothing
rather than blocking.

**For a Codex builder the sandbox is deliberately left on** (`autonomy: auto` maps to
`--sandbox workspace-write --ask-for-approval never`). Turning approvals off is not the
same as turning the sandbox off: the builder still cannot write outside its workspace or
reach the network, it simply gets a hard error instead of a dialog —

```
zsh:1: operation not permitted: /Users/you/.some-file
```

— which the model reads and works around on its own. This matters, because the whole relay
([How the relay works](relay.md)) exists precisely because the builder is sandboxed. Widening `--sandbox` to
`danger-full-access` would let it reach the Herdr socket directly and make the mailbox
redundant; `guide/design.md` explains why that is a bad trade for an agent executing plans
autonomously.

**opencode has no sandbox at all.** Its `--auto` is a boolean auto-approve, not a
permission level, so an opencode builder is unconfined by construction — it can reach the
Herdr socket directly, which is why it needs no relay. Choose it knowing that; the
protection a Codex builder gets from `workspace-write` simply does not exist there.

Override autonomy for a one-off session, but the defaults are the supported path:

```bash
tandem-pair --builder-autonomy full          # preferred
HERDR_BUILDER_AUTONOMY=full tandem-pair      # same, via the environment
```

`HERDR_BUILDER_SANDBOX` and `HERDR_BUILDER_APPROVAL` still work for a Codex builder and
are translated to the nearest autonomy value with a deprecation warning.

`tandem-pair` is idempotent — re-run it any time. It adopts an already-running agent rather
than starting a second one, and after a server restart it is how you get the names back.

## Adoption

When one unnamed agent is already running in the planner's tab, `tandem-pair` adopts it instead
of starting another. Agents in other tabs are never adopted, and `--builder KIND` adopts only
an agent of that kind. An adopted agent keeps the settings it was originally started with:
the model, effort and autonomy declared in `.tandem.json` are reported as unenforced rather
than applied retroactively. Restart that pane when those configured settings must take effect.
A reviewer is adopted only when an agent already holds the reviewer's name (`reviewer`, or
`HERDR_REVIEWER_NAME`). It keeps the settings it was started with, and a declared model or
effort it does not run with is reported as unenforced. Close it and re-pair to apply them.

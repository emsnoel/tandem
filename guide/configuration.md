# Configuration

## Per-project configuration

`.tandem.json` at the repo root. Project settings and role intent may be declared here.

```json
{
  "plans_dir": "docs/plans",
  "check_command": "cargo test",
  "check_paths": ["src", "internal"],
  "conventions_file": "AGENTS.md",
  "builder": {
    "kind": "opencode",
    "model": "anthropic/claude-sonnet-5",
    "autonomy": "full",
    "allow_degrade": ["effort"]
  },
  "reviewer": {
    "kind": "codex",
    "model": "gpt-5.5",
    "effort": "high"
  },
  "prompt_extra": "Extra instructions appended to the builder's launch prompt.",
  "task_input_budget": 3000000,
  "context_limit": 24000,
  "build_engine": "durable"
}
```

The `builder` block accepts `kind`, `model`, `effort`, `autonomy`, and `allow_degrade`.
The legacy top-level `builder_kind` key still works.

The `reviewer` block accepts the same keys, and `kind` is required. Every other key here
is optional. A reviewer is needed, because review and build refuse without one, so declare
it here or pass `tandem-pair --reviewer KIND` each session. A declared reviewer always starts with a plain
`tandem-pair`, and there is no flag to skip it. `tandem-pair --reviewer`,
`--reviewer-model` and `--reviewer-effort` win over the block for that session only. A
block with no `kind`, and no `--reviewer` or `HERDR_REVIEWER_KIND` to supply one, stops the
pairing and names the missing key. `args`, `path` and `allow_widen` in the block are
ignored with a warning, as in the `builder` block. Kind approval covers the reviewer. A
repository-declared kind you have not approved here refuses with exit 22 until
`tandem-pair --approve-kind KIND`. A Copilot builder with a Codex reviewer is approved in one
run with `tandem-pair --approve-kind copilot --approve-kind codex`.

`check_paths` is optional and scopes `tandem-unpair`'s gate 8 tree-staleness
check to just those paths instead of the whole tree; unset (or not a JSON array)
means the whole tree, same as before this key existed. It has no detection —
unlike `check_command`, there is no reasonable default to guess, so it is set or
it isn't.

`task_input_budget` is the input tokens one build task may use, subagents included, before
tandem stops the build for a human to look (default 3,000,000; 0 turns it off). See
[Per-task builds](builds.md#per-task-builds).

`build_engine` is `"legacy"` or `"durable"`: the engine a new build here starts on, so no one
has to set `TANDEM_BUILD_ENGINE`. The variable overrides it when set. An unknown value is
refused. Once a build has started its relay decides, and this key is not read again.
`tandem state engine` and `tandem-status` show which engine the next build would use and why.

`context_limit` is the most bytes of prompt a durable run gives the builder for one task
(the engine's default, 24,000, when unset or 0). `tandem-handoff --context-limit N`
overrides it for one handoff. A durable start measures every task's first prompt against it
before it creates anything, and refuses with each task that does not fit and the limit that
would. The required part of a prompt (the task's brief, the plan's opening section and
tandem's instructions) is never cut to fit, so a limit below a real task only blocks it.

### Files renamed from `herdr-`

Three files tandem keeps were named after Herdr before the project settled on its own
name. Existing repos and machines already hold them, so all three resolve as **the new
name, unless only the old one is there**:

| Now | Was | Where |
| --- | --- | ----- |
| `.tandem.json` | `.herdr.json` | repo root — this manifest |
| `.tandem-relay/` | `.herdr-relay/` | repo root — the mailbox (a builder paired from a subdirectory also gets one there, holding its identity and reviews; see guide/design.md) |
| `.tandem-hook-baseline` | `.herdr-hook-baseline` | `$CODEX_HOME` |

Nothing is migrated: a repo that has `.herdr.json` keeps using it, and `tandem-init
--force` rewrites that same file rather than leaving two manifests disagreeing. Ask which
one is in play rather than guessing:

```bash
tandem paths manifest        # the manifest for this repo
tandem paths relay           # the mailbox directory
tandem paths hook-baseline   # the recorded Codex hook baseline
```

**If your repo still has `.herdr.json`, edit that file — do not create a `.tandem.json`
beside it.** The new name wins when both exist, so a fresh one would take effect while
every other setting in the old file quietly stopped being read. To move a repo over,
rename the file rather than adding one:

```bash
git mv .herdr.json .tandem.json
```

Resolution lives in `internal/paths`, in one place, and the bash commands reach it through
`tandem paths` rather than restating it. That matters most for the mailbox: if the bridge
and the asking agent ever disagreed about which directory it is, questions would go
unanswered with nothing logged.

**There is no `planner` block, deliberately.** The planner is whichever Claude Code agent
is running in your pane (`tandem-pair` looks for one and fails if there is none), so there
is nothing for a repository to select. The configuration layer can already resolve a
planner role, but nothing launches one, and a key that is read and then ignored is worse
than one that says no — so it says no. A `planner` block in `.tandem.json` is refused with a warning naming
`defaults.planner_kind` in your user config, which is where a value describing your
own terminal belongs. Making the planner pluggable is designed but
unbuilt. This repository deliberately keeps
using `builder_kind: "codex"` so its own teardown continuously exercises that
backward-compatibility path; do not tidy it into a role block just for consistency. Its
`reviewer` block, with `kind: "codex"`, is there because review and build refuse without one.

Detection, when a key is absent:

- **plans_dir** — first of `docs/superpowers/plans`, `docs/plans`, `plans`, `.plans`
- **check_command** — `package.json` scripts (`check`, `verify`, `ci`, `test`), else
  `Cargo.toml`, `go.mod`, `pyproject.toml`, else a `check:`/`test:` target in a
  justfile or Makefile
- **conventions_file** — `AGENTS.md`, else `CONTRIBUTING.md`
- **check_paths** — no detection; absent means the whole tree, as it always has.

`prompt_extra` is where project-specific instructions belong — hardware you cannot test
on, deployment rules, anything that is true of one repo and not the others.

## Choosing an agent

`kind` selects `codex`, `claude`, `copilot`, or `opencode`. Configure behavior with the
normalized autonomy vocabulary; `tandem` maps it onto the selected CLI:

| autonomy | Codex | Claude Code | Copilot | opencode |
| -------- | ----- | ----------- | ------- | -------- |
| `readonly` | `--sandbox read-only --ask-for-approval on-request` | `--permission-mode plan` | `--mode plan` | unsupported |
| `ask` | `--sandbox workspace-write --ask-for-approval on-request` | `--permission-mode manual` | *no flag — Copilot's own default* | omit `--auto` |
| `auto` | `--sandbox workspace-write --ask-for-approval never` | `--permission-mode auto` | `--allow-all-tools --allow-all-urls`, plus `--allow-all-paths` for builder and reviewer | unsupported |
| `full` | `--dangerously-bypass-approvals-and-sandbox` | `--permission-mode bypassPermissions` | `--allow-all` | `--auto` |

OpenCode has no filesystem sandbox and no effort control on its interactive TUI path.
Its only faithful autonomy values are therefore `ask` and `full`; an effort request is
refused unless `allow_degrade` permits dropping that axis.

**`readonly` is a planner value, and an unattended builder will stall on it.** This is
true of every adapter, not just one: `readonly` withholds authority, so Codex serves it as
`--ask-for-approval on-request`, Claude Code as `--permission-mode plan`, and Copilot as
`--mode plan`. All three prompt. Measured with a Copilot builder on 2026-09-04: it launched
cleanly in plan mode and then blocked on an approval dialog for `git status` — a read-only
command, but still one the CLI asks about. That is the vocabulary working as designed
rather than a mapping fault: the autonomy axis is *authority*, and `internal/agent` is
explicit that convenience and authority are separate questions. Give a builder `auto`;
`readonly` belongs to a role with a human watching it.

**Copilot has no filesystem sandbox on the default path either.** Its `--allow-all-paths`
governs the built-in file tools; shell commands run with your full user access unless the
experimental MXC sandbox is on, which it is not by default. A Copilot builder is therefore
unconfined in the same way an opencode one is — choose it knowing that.

**Copilot's `ask` is the one value tandem cannot command.** There is no `--permission-mode`
flag: manual approval is the CLI's default, and `defaultPermissionMode` in
`~/.copilot/settings.json` decides it. So `autonomy: ask` is honoured by emitting nothing,
and a machine that has set that key to `allow-all` will run more permissively than the
repository asked. `auto` and `full` are unaffected — their flags override the setting.

**A Copilot builder or reviewer is launched with `--no-ask-user`.** The `ask_user` tool
asks a human; an unattended builder or reviewer has none, so a call to it is a pane that
reports `idle` and waits forever. `tandem-ask planner` is the channel that reaches somebody,
and the builder and the reviewer already have it. A planner-role Copilot keeps the tool.

**A Copilot builder or reviewer is also given a fresh `--session-id`.** Copilot shows a
session-restore picker whenever `$COPILOT_HOME/open-sessions-state.json` is
non-empty, and tearing a pair down leaves an `Interrupted` entry there — so
every pairing after the first would meet it. **Herdr reports a pane blocked on
that picker as `idle`**, which means `tandem-pair` says `pair ready`,
`tandem-handoff` says the plan was delivered, and the plan is swallowed by a
modal. Naming a new session id skips the picker entirely. A planner-role
Copilot is left alone: it is somebody's own pane, and forcing a new session
would discard the conversation they are in.

Override a builder for one pairing with flags:

```bash
tandem-pair --builder opencode --builder-autonomy full
```

### The reviewer

The reviewer takes the same `kind` values and the same autonomy table. It resolves from its
own sources only, in this order: a `tandem-pair --reviewer*` flag, then
`HERDR_REVIEWER_KIND`, `HERDR_REVIEWER_MODEL`, `HERDR_REVIEWER_EFFORT` or
`HERDR_REVIEWER_AUTONOMY`, then the `reviewer` block, then the adapter's default for its
kind. Default autonomy is `auto`, as for the builder, because the reviewer runs unattended
and `readonly` would stall it. There is no default reviewer kind and no
`defaults.reviewer_kind`.

Only the `reviewer` block or `--reviewer` turns a reviewer on. The `HERDR_REVIEWER_*`
variables refine a reviewer that is already on, and `HERDR_REVIEWER_KIND` alone starts
nothing. The reviewer never reads the `builder` block, `HERDR_BUILDER_*`,
`defaults.builder_kind`, or the `model`, `effort` and `autonomy` of your user config's
`adapters.<kind>` entry, which are the builder's choices. The `args`, `path`,
`allow_widen` and `allow_degrade` of that entry do apply to the reviewer by its kind,
because they describe the CLI on this machine.

```bash
tandem-pair --reviewer claude --reviewer-model opus --reviewer-effort high
```

## User configuration

Local adapter configuration lives in `~/.config/herdr/tandem.json`:

```json
{
  "defaults": { "builder_kind": "opencode" },
  "adapters": {
    "codex": {
      "path": "/opt/homebrew/bin/codex",
      "args": ["-c", "model_reasoning_effort=high"]
    },
    "opencode": { "allow_widen": ["autonomy"] }
  }
}
```

A repository may declare intent, but it may not grant capability. `allow_widen`, raw
`args`, and executable `path` are accepted only from your user configuration; the same
keys in `.tandem.json` are ignored with a warning.

# Configuration

## Per-project configuration

`.tandem.json` sits at the repo root. It holds the project's settings, and the agents the
project wants for each role.

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

Every key is optional, except `kind` inside a `reviewer` block.

The `builder` block takes `kind`, `model`, `effort`, `autonomy` and `allow_degrade`. The
older top-level `builder_kind` key still works.

The `reviewer` block takes the same keys. You need a reviewer, because reviews and builds
refuse without one. Declare it here, or pass `tandem-pair --reviewer KIND` every session.

- A declared reviewer always starts with a plain `tandem-pair`. No flag skips it.
- `tandem-pair --reviewer`, `--reviewer-model` and `--reviewer-effort` override the block
  for that session only.
- A block with no `kind` stops the pairing and names the missing key, unless
  `--reviewer` or `HERDR_REVIEWER_KIND` supplies one.
- tandem ignores `args`, `path` and `allow_widen` in the block and prints a warning, as it
  does for the `builder` block.

Kind approval covers the reviewer too. If the repository declares a kind you haven't
approved on this machine, pairing refuses with exit 22 until you run
`tandem-pair --approve-kind KIND`. To approve a Copilot builder and a Codex reviewer in one
run, use `tandem-pair --approve-kind copilot --approve-kind codex`.

`check_paths` limits gate 8 of `tandem-unpair`, which checks whether the tree changed while
the check ran. With `check_paths` set, the gate looks only at those paths. Unset, or not a
JSON array, it looks at the whole tree. tandem doesn't guess a value, because unlike
`check_command` there is no sensible default.

`task_input_budget` is how many input tokens one build task may use, subagents included,
before tandem stops the build for a person to look. The default is 3,000,000, and 0 turns it
off. See [What stops a build](builds.md#what-stops-a-build).

`build_engine` is `"legacy"` or `"durable"`. It sets the engine new builds in this repo
start on, so nobody has to set `TANDEM_BUILD_ENGINE`. The variable overrides the key when
set. tandem refuses an unknown value. Once a build has started, its relay decides, and
tandem doesn't read this key again. `tandem state engine` and `tandem-status` show which
engine the next build would use, and why.

`context_limit` is the largest prompt, in bytes, that a durable run gives the builder for
one task. When unset or 0, the engine's default of 24,000 applies.
`tandem-handoff --context-limit N` overrides it for one handoff. A durable start measures
every task's first prompt against the limit before it creates anything. If a task doesn't
fit, it refuses, naming each task that doesn't fit and the limit that would. tandem never
cuts the required part of a prompt to fit: the task's brief, the plan's opening section and
tandem's instructions. So a limit smaller than a real task only blocks that task.

`prompt_extra` holds instructions for this project only, such as hardware the builder can't
test on, or deployment rules.

When a key is missing, tandem detects it like this:

- **plans_dir** is the first of `docs/superpowers/plans`, `docs/plans`, `plans` and
  `.plans` that exists.
- **check_command** comes from `package.json` scripts (`check`, `verify`, `ci`, `test`), or
  else `Cargo.toml`, `go.mod` or `pyproject.toml`, or else a `check:` or `test:` target in a
  justfile or Makefile.
- **conventions_file** is `AGENTS.md`, or else `CONTRIBUTING.md`.
- **check_paths** has no detection. Without it, gate 8 checks the whole tree.

**There is no `planner` block, on purpose.** The planner is whichever Claude Code agent is
running in your pane. `tandem-pair` looks for one and fails if there is none, so there is
nothing for a repository to choose. The config code can already resolve a planner role,
but nothing launches one. A key that tandem reads and then ignores is worse than one it
refuses, so it refuses. A `planner` block in `.tandem.json` gets a warning that points to
`defaults.planner_kind` in your user config, which is where a setting about your own
terminal belongs. A configurable planner is designed but not built yet.

This repository keeps `builder_kind: "codex"` on purpose, so its own teardown keeps testing
that older key. Don't change it to a `builder` block for consistency. Its `reviewer` block,
with `kind: "codex"`, is there because reviews and builds refuse without one.

### Files renamed from `herdr-`

Three of tandem's files were named after Herdr before tandem had its own name. Existing
repos and machines already have them, so for each one tandem uses **the new name, unless
only the old one exists**:

| Now | Was | Where |
| --- | --- | ----- |
| `.tandem.json` | `.herdr.json` | The repo root. This is the manifest described above |
| `.tandem-relay/` | `.herdr-relay/` | The repo root. This is the mailbox. A builder paired from a subfolder also gets one there, holding its identity and reviews (see `guide/design.md`) |
| `.tandem-hook-baseline` | `.herdr-hook-baseline` | `$CODEX_HOME` |

tandem migrates nothing. A repo that has `.herdr.json` keeps using it, and
`tandem-init --force` rewrites that same file instead of creating a second manifest that
disagrees with it. Ask tandem which file is in use instead of guessing:

```bash
tandem paths manifest        # the manifest for this repo
tandem paths relay           # the mailbox folder
tandem paths hook-baseline   # the recorded Codex hook baseline
```

**If your repo still has `.herdr.json`, edit that file. Don't create a `.tandem.json` beside
it.** The new name wins when both exist, so tandem would read the new file and silently stop
reading every other setting in the old one. To move a repo over, rename the file:

```bash
git mv .herdr.json .tandem.json
```

`internal/paths` is the one place that decides these names, and the bash commands ask it
through `tandem paths` instead of repeating the rule. That matters most for the mailbox. If
the bridge and the asking agent ever disagreed about which folder it is, questions would go
unanswered and nothing would be logged.

## Choosing an agent

`kind` selects `codex`, `claude`, `copilot` or `opencode`. You set behaviour with one shared
autonomy setting, and tandem maps it onto each CLI's own flags:

| autonomy | Codex | Claude Code | Copilot | opencode |
| -------- | ----- | ----------- | ------- | -------- |
| `readonly` | `--sandbox read-only --ask-for-approval on-request` | `--permission-mode plan` | `--mode plan` | unsupported |
| `ask` | `--sandbox workspace-write --ask-for-approval on-request` | `--permission-mode manual` | *no flag, which is Copilot's own default* | omit `--auto` |
| `auto` | `--sandbox workspace-write --ask-for-approval never` | `--permission-mode auto` | `--allow-all-tools --allow-all-urls`, plus `--allow-all-paths` for the builder and reviewer | unsupported |
| `full` | `--dangerously-bypass-approvals-and-sandbox` | `--permission-mode bypassPermissions` | `--allow-all` | `--auto` |

opencode has no filesystem sandbox, and no effort setting in its interactive TUI. So the
only autonomy values it can match are `ask` and `full`. tandem refuses an effort setting
for opencode unless `allow_degrade` allows dropping it.

**`readonly` is for a planner, and an unattended builder stalls on it.** This is true of
every adapter. `readonly` withholds authority, so Codex runs it as
`--ask-for-approval on-request`, Claude Code as `--permission-mode plan`, and Copilot as
`--mode plan`. All three ask for approval. On 2026-09-04 we tested a Copilot builder. It
started cleanly in plan mode, then stopped at an approval dialog for `git status`, a
read-only command that the CLI still asks about. That is the setting working as designed,
not a mapping bug. Autonomy is about authority, and `internal/agent` keeps convenience and
authority as separate questions. Give a builder `auto`, and keep `readonly` for a role with
a person watching it.

**Copilot has no filesystem sandbox by default either.** Its `--allow-all-paths` covers the
built-in file tools only. Shell commands run with your full user access, unless the
experimental MXC sandbox is on, and it is off by default. So a Copilot builder is unconfined
in the same way as an opencode one. Choose it knowing that.

**Copilot's `ask` is the one value tandem can't enforce.** Copilot has no
`--permission-mode` flag. Manual approval is its default, and `defaultPermissionMode` in
`~/.copilot/settings.json` decides it. So tandem honours `autonomy: ask` by passing no flag,
and a machine that set that key to `allow-all` will run with more permission than the
repository asked for. `auto` and `full` are not affected, because their flags override the
setting.

**tandem starts a Copilot builder or reviewer with `--no-ask-user`.** The `ask_user` tool
asks a person, and an unattended builder or reviewer has nobody to ask. A call to it would
leave the pane reporting `idle` and waiting forever. `tandem-ask planner` reaches a person,
and the builder and reviewer already have it. A Copilot planner keeps the tool.

**tandem also gives a Copilot builder or reviewer a fresh `--session-id`.** Copilot shows a
session-restore picker whenever `$COPILOT_HOME/open-sessions-state.json` isn't empty, and
closing a pair leaves an `Interrupted` entry there. So every pairing after the first would
hit the picker. **Herdr reports a pane stuck on that picker as `idle`.** `tandem-pair` would
say `pair ready`, `tandem-handoff` would say it delivered the plan, and the picker would
swallow the plan. A new session id skips the picker. tandem leaves a Copilot planner alone,
because that pane is somebody's own, and a new session would throw away the conversation
they are in.

To override the builder for one pairing, use flags:

```bash
tandem-pair --builder opencode --builder-autonomy full
```

### The reviewer

The reviewer takes the same `kind` values and the same autonomy table. It reads only its own
settings, in this order:

1. a `tandem-pair --reviewer*` flag;
2. `HERDR_REVIEWER_KIND`, `HERDR_REVIEWER_MODEL`, `HERDR_REVIEWER_EFFORT` or
   `HERDR_REVIEWER_AUTONOMY`;
3. the `reviewer` block;
4. the adapter's default for its kind.

The default autonomy is `auto`, as for the builder, because the reviewer runs unattended
and `readonly` would stall it. There is no default reviewer kind, and no
`defaults.reviewer_kind`.

Only the `reviewer` block or `--reviewer` turns a reviewer on. The `HERDR_REVIEWER_*`
variables adjust a reviewer that is already on, and `HERDR_REVIEWER_KIND` alone starts
nothing.

The reviewer never reads the builder's choices: the `builder` block, `HERDR_BUILDER_*`,
`defaults.builder_kind`, or the `model`, `effort` and `autonomy` in your user config's
`adapters.<kind>` entry. The `args`, `path`, `allow_widen` and `allow_degrade` in that entry
do apply to the reviewer of that kind, because they describe the CLI on this machine.

```bash
tandem-pair --reviewer claude --reviewer-model opus --reviewer-effort high
```

## User configuration

Your own adapter settings live in `~/.config/herdr/tandem.json`:

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

A repository can ask for settings, but it can't grant extra authority. tandem accepts
`allow_widen`, raw `args` and an executable `path` only from your user config. It ignores
the same keys in `.tandem.json` and prints a warning.

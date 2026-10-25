# tandem

Two agents in tandem under [Herdr](https://herdr.dev). **Claude Code plans; the builder
can be any supported agent CLI** — `codex`, `claude`, `copilot` or `opencode` — with its model,
effort and autonomy declared per repository. A third pane, the reviewer, reviews every plan
and every task on its own model. The builder never reviews. Either side can ask the other a
question without a human relaying it. One working tree, one branch. The pair shares a checkout
rather than diverging into worktrees, so there is nothing to merge back.

Repo-agnostic. The scripts know nothing about any project; everything project-shaped is
detected or declared in that repo's `.tandem.json`.

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/emsnoel/tandem/main/install.sh | bash
```

This installs the latest release. You need `curl`, `tar`, `jq` and `shasum` (or
`sha256sum`), and no Go. `install.sh` downloads the tarball for your machine (macOS or
Linux, arm64 or amd64), checks it against the release's `SHA256SUMS`, and unpacks it into
`~/.local/share/tandem/<version>` (`$XDG_DATA_HOME` and `TANDEM_SHARE_DIR` move it).
`~/.local/share/tandem/current` points at that version, and the `tandem` binary and the
twelve commands in `~/.local/bin` are symlinks into it. Nothing is replaced until the
download has passed its checksum and the binary has run, so a failed upgrade leaves the
old version working.

Re-run the same line to upgrade. Only the version just installed is kept. To install a
particular release, or a pre-release, name it:

```bash
curl -fsSL https://raw.githubusercontent.com/emsnoel/tandem/main/install.sh | bash -s -- --version v0.1.0
```

`install.sh` takes an optional destination (`bash -s -- ~/bin`) and defaults to
`~/.local/bin`. It warns if that directory is not on `PATH`, because the Herdr keybindings
resolve these by bare name.

**It will not overwrite an existing Herdr config.** If `~/.config/herdr/config.toml` does
not exist it writes the example there. If it does exist, it leaves it completely alone and
instead lists which of the settings this harness uses are missing, so you can copy across
only the parts you want. Your theme, prefix key and layout are yours; none of the harness
settings are required for the commands to work, they only add keybindings, notifications
and the role rows in the sidebar. (`HERDR_CONFIG_PATH` is honoured if you keep the config
elsewhere.)

### Building from source

To work on tandem itself, clone it and install from the checkout. This needs Go 1.26+.

```bash
git clone https://github.com/emsnoel/tandem.git
cd tandem
./install.sh --from-source
```

`--from-source` symlinks the twelve commands into the checkout, so an edit under `bin/` is
live at once. **The symlinks store absolute paths. If you move or rename the clone,
re-run `./install.sh --from-source` from its new location.** Plain `./install.sh` in a
checkout installs a release instead, and says so.

**The `tandem` binary is compiled and copied, so it can go stale.** If you edit the Go
sources you must re-run `./install.sh --from-source`. The build stamps a hash of its
inputs (`cmd/`, `internal/`, `go.mod`, `go.sum`, embedded SQL and templates) into the
binary, and `tests/run.sh` asserts it matches the tree, so a stale binary fails the check
command, which `tandem-unpair` runs before any teardown. You will be told, rather than
quietly running yesterday's gates. `tandem version` prints `dev` for a source build and
the tag for a release.

Versions follow [guide/versioning.md](guide/versioning.md), and
[guide/releasing.md](guide/releasing.md) says how a release is cut.

Then, in each repo you want to pair in:

```bash
tandem-init
```

## Upgrading: a reviewer is now required

The builder no longer reviews anything, on either build engine. Four things change:

- Every repository needs a reviewer before its next review. Add a `reviewer` block to
  `.tandem.json` (see [Per-project configuration](#per-project-configuration)), or pass
  `tandem-pair --reviewer KIND` every session. Tandem's own `.tandem.json` declares a Codex
  reviewer.
- `tandem-review --builder` and `tandem-verdict --role builder` are gone. Both refuse and
  say why.
- Reviews written before the upgrade do not clear plans. The builder wrote them, so the gate
  refuses with `builder_review` and `tandem-handoff` asks the reviewer for a new review.
- A legacy or durable build without a reviewer pane no longer starts. A durable run started
  before the upgrade pauses with `no_reviewer`. Stop it (`tandem build stop`), end its open
  jobs (`tandem state recover --run ID`), archive it (`tandem state archive --run ID`),
  re-pair with a reviewer, and hand off again.

## Commands

Every command has two spellings that do exactly the same thing:

```bash
tandem pair          # what a human types
tandem-pair          # the script itself, and what keybindings and scripts use
```

`tandem <verb>` is the compiled binary handing over to `tandem-<verb>` on `PATH`. It is
`execve`, not a subprocess, so the script replaces `tandem` entirely and its exit code —
which for this harness carries meaning, see [When it refuses](#when-it-refuses) — arrives unchanged.
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
| `tandem-verdict`         | Returns a review to the planner with a verdict. Run by the reviewer pane at the end of a review, and refused from any other pane (see [Which pane is the reviewer](#which-pane-is-the-reviewer)). `tandem-review` writes `--role NAME`, the reviewer's recorded name, into the command, because Codex runs commands without the pane's `HERDR_ROLE` |
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
[Choosing an agent](#choosing-an-agent) and `guide/architecture/review-handback.md`.

The binary also has subcommands that the scripts call. `agent watch` and the bridge read
Herdr's event stream on its socket. `build wait` reads the run's ledger, and `pane wait-shell`
reads the pane's process info:

| Subcommand | Does |
| ---------- | ---- |
| `tandem agent watch [NAME...] [--root DIR] [--mailbox DIR] --timeout MS` | Waits for one of the named agents to change status. Prints `changed<TAB>NAME<TAB>STATUS` and exits 0, or prints `timeout` and exits 0. Exits 27 with `agent_exited<TAB>NAME` when an agent is gone, and 3 when the stream breaks or Herdr is unreachable. `--root` adds the durable run's builder and reviewer. `--mailbox` also returns on a message file in the relay's queues (`queued<TAB>PATH`), a change of its run marker (`run<TAB>PATH`), or a write to the run's state (`nudge<TAB>PATH`), and exits 1 when the file watcher cannot start |
| `tandem agent self [--expect NAME [--claimed ROLE] [--timeout MS]]` | Prints the name of the agent in this pane (`HERDR_PANE_ID`), or nothing. With `--expect`, prints whether this shell is the reviewer pane NAME instead: `accept` or `refuse`, a reason and a detail, separated by tabs. See [Which pane is the reviewer](#which-pane-is-the-reviewer) |
| `tandem agent prompt NAME TEXT --as KIND [--wait-unblocked MS]` | Sends the prompt the way KIND is delivered and confirms the turn ran. KIND is task, review, handback, verdict, question or notice, and each kind's checks are one row in `cmd/tandem/deliver.go`. The planner's kinds wait up to 2 minutes for a blocked agent, or for a pane showing a subagent or a list, before typing. `--wait-unblocked` sets that wait in MS and needs `--as`. Every `tandem agent prompt` refuses such a pane with `agent_input_elsewhere` (see [below](#a-pane-showing-a-subagent-or-a-list)) |
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

## When it refuses

Every refusal prints the exact command or edit that resolves it, so the message is
usually enough on its own. The codes, for reference:

| Exit | Means | Fix |
| ---- | ----- | --- |
| 3 | the Herdr socket is unreachable from this shell — nothing was delivered | check the Herdr server is running, and run from a Herdr-managed pane |
| 4–9 | a teardown gate refused | see [step 7](#7-the-builder-hands-back-and-the-planner-closes-the-session) |
| 7 | from `tandem-review` or `tandem-handoff`: the prompt was typed but the pane never started working, usually at a native dialog Herdr cannot see | clear the dialog in the named pane and re-run |
| 8 | from `tandem-review` or `tandem-handoff`: the pane started a turn and it ended within 8 seconds with no new tokens in the conversation's log, so the model did no work. A model the CLI rejects, an expired login or used-up quota looks like this. The message shows the pane's last 20 lines | fix what the pane shows, or re-run `tandem-pair` with another model, then re-run the command |
| 20 | the value would have to **narrow** — the adapter cannot give the builder the authority you asked for, and the pane would stall | pick a supported value, or add the axis to `allow_degrade` in the repo manifest — `tandem paths manifest` names it |
| 21 | the value would have to **widen** — the adapter would grant authority you did **not** ask for | pick a supported value, or add the axis to `allow_widen` in your *user* config |
| 22 | the repository asked for an agent `kind` you have not approved **for that repository** | `tandem-pair --approve-kind <kind>`, once |
| 23 | the builder or reviewer would launch into a gate that needs a human. Also from `tandem build wait` and `tandem pane wait-shell` when the timeout passes first, and from a legacy `tandem build start` when `reviewer.json` records no reviewer pane | clear the gate. Usually that means running the agent once in the directory and accepting its trust dialog. For a missing reviewer, declare a `reviewer` block and re-run `tandem-pair`, or run `tandem-pair --reviewer KIND` |
| 24 | the plan has not cleared review — there is no reviewer (checked first, whether or not a review exists), no review exists for it, the plan changed since it was reviewed, the recorded builder identity changed, the review was not written by the reviewer, the reviewer's kind or model changed since, or the reviewer's objections stand. Overruling a review that a newer one has since superseded refuses the same way, naming the newer one | `tandem-review <plan>`, fix the plan, or `tandem-handoff --override-review <id> --because "..."` — read the newer review first if you are told it has superseded the one you named |
| 25 | from `tandem agent prompt --as`: the prompt never started a turn. The scripts report this as their own 7 | as for 7 |
| 26 | from `tandem agent prompt --as`: the turn ended at once with no model work. The scripts report this as their own 8 | as for 8 |
| 27 | from `tandem agent alive`: an agent that was just started quit, so Herdr stopped listing it or its pane is back at the shell. From `tandem agent watch`: a watched agent was released or its pane closed, or it was already gone when the watch subscribed. `tandem-pair` runs this after starting each agent and stops with the pane's last lines | fix what the pane shows, then re-run `tandem-pair` |

### A pane showing a subagent or a list

Herdr reports a pane's status, not which conversation its input reaches. Each agent CLI
has screens where Herdr reads `idle` (or `working`) but typed text misses the main
conversation. Once, the planner pane showed a background subagent's screen, and the
bridge's verdict reached that subagent nine minutes later instead of the planner. The
screens, each checked live on 2026-10-17:

| Kind | Screen | What typed text does there |
| ---- | ------ | -------------------------- |
| Claude Code | a subagent's screen (agent list, then Enter) | goes to that subagent |
| Claude Code | the agent list holding the keys (`↑/↓ to select`) | stays in the input, unsent |
| Codex | a sub-agent's thread (`⌥←` / `⌥→`) | stays in the input; input is disabled |
| Codex | the agent command center (`←`, `/agent`) | its Enter opens another session |
| Copilot | a subagent's screen (`/tasks`, then Enter) | goes to that subagent, which answers it |
| Copilot | the task list, the focused sidebar, the Sessions tab | single keys act: `x` kills a task or closes a session |
| Copilot | a GitHub tab (Issues, Pull requests, Gists) | goes nowhere |
| opencode | a child session (`ctrl+x down`) | dropped; child sessions cannot be prompted |

Before typing, `tandem agent prompt`, a durable run's send and `tandem agent reset` read
the pane's screen (`internal/agent/screen.go`) and type nothing into one of these. The
refusal is `agent_input_elsewhere` (exit 10), and its message names what the pane
shows. `tandem-handback` and `tandem-ask` exit 3, the bridge logs the failure and leaves
an `.UNDELIVERED` marker carrying the reason, a durable run tries again on its next
tick, and a reset reports `skip:off-main`. Return the pane to its main conversation and
re-run. Don't press Esc to leave a Claude subagent's screen with its input focused: Esc
there interrupts the subagent. Use the agent list (`↓`, pick `main`, Enter).

**Why is it using that model?** A value can come from a flag, the environment,
`.tandem.json`, your user config, or an adapter default — in that order. Ask rather than
guess; every value reports where it came from:

```bash
tandem config resolve --role builder --root .
```

```json
{"kind":{"value":"codex","from":"repo"},"model":{"value":"gpt-5.4","from":"env"}, ...}
```

`from: "adopted"` means the agent was adopted rather than launched, so that value
describes what it is *actually* running with — the configured one is not in force.

The reviewer resolves on its own, and never from a builder source:

```bash
tandem config resolve --role reviewer --root .
```

It always exits 0 and reports one of three outcomes in `status`. `none` means no `reviewer`
block and no `--kind`. `resolved` carries the values with provenance, as for the builder.
`invalid` carries the reason, such as a block with no `kind`. It takes `--kind`, `--model`,
`--effort` and `--autonomy` as overrides, as `tandem-pair` passes its `--reviewer*` flags.

## A worked example

Shipping a feature called "add rate limiting".

### 1. One-time setup in the target repo

```bash
cd ~/projects/my-app
tandem-init            # writes .tandem.json — detected or declared
tandem-pair            # names this Claude pane "planner", starts/adopts a sibling
                      # Codex pane named "builder" and the declared "reviewer",
                      # starts the relay bridge
herdr agent list      # confirm all three names appear
```

**Point the builder at the repo if your planner pane is not already in it.** `tandem-pair`
takes the builder's working directory from the planner agent's own cwd — where Claude Code
is running, which is not necessarily where you just `cd`-ed. So:

```bash
tandem-pair --cwd ~/projects/my-app
```

Get this wrong and the builder starts outside the repo, where its conventions file is
missing and `plans_dir` does not resolve — both of which surface as confusing failures
later rather than an error now. `--cwd` is validated as a directory, and
`tandem-pair --dry-run` shows the path it will use. It is also the directory `tandem-pair`
records as trusted for Codex, so point it at the repo you actually mean.

### How the builder is launched unattended

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
below exists precisely because the builder is sandboxed. Widening `--sandbox` to
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

#### Adoption

When one unnamed agent is already running in the planner's tab, `tandem-pair` adopts it instead
of starting another. Agents in other tabs are never adopted, and `--builder KIND` adopts only
an agent of that kind. An adopted agent keeps the settings it was originally started with:
the model, effort and autonomy declared in `.tandem.json` are reported as unenforced rather
than applied retroactively. Restart that pane when those configured settings must take effect.
A reviewer is adopted only when an agent already holds the reviewer's name (`reviewer`, or
`HERDR_REVIEWER_NAME`). It keeps the settings it was started with, and a declared model or
effort it does not run with is reported as unenforced. Close it and re-pair to apply them.

### 2. The planner does the thinking

You brainstorm and write an implementation plan into whatever `plans_dir` resolved to, e.g.
`docs/plans/2026-08-29-rate-limiting.md`.

**This part is deliberately out of scope.** The harness automates the handoff and the Q&A
transport, not the thinking, which is why `tandem-handoff` has no opinion on plan format —
it just picks a file.

### 3. The reviewer attacks the plan

```bash
tandem-review                          # most recently reviewed plan, else newest; same rule as tandem-handoff
tandem-review docs/plans/specific.md   # or name one
```

The reviewer pane reads the plan and the code it touches, probes what it can actually
do here, and hands back a verdict with `tandem-verdict --plan ... --role NAME
--review ... --verdict clean|objections`. It is looking for five things and nothing
else: a task that needs something a later task introduces, two tasks that contradict
each other, a step that cannot execute as written, a step that needs authority the
builder does not have, and a plan whose own test or example would not produce the result it
claims.

It is not asked for design opinions, and does not give them. That narrowing is
why a review is a handful of findings rather than a wall of preferences.

**The builder never reviews.** With no reviewer recorded in `reviewer.json`,
`tandem-review` sends nothing and names both remedies: declare a `reviewer` block in
`.tandem.json` and re-run `tandem-pair`, or run `tandem-pair --reviewer KIND`. There is no
`--builder` flag, and passing one is refused. `tandem-verdict` refuses a verdict from
any pane but the reviewer, as below.

#### Which pane is the reviewer

`tandem-verdict` and `tandem-handback --review` record a review only from the reviewer
pane. Both ask `tandem agent self --expect NAME`, where NAME is the reviewer's recorded
name, and tandem decides:

- With no `HERDR_PANE_ID`, the claimed role decides. That is `--role`, then `HERDR_ROLE`,
  and it must be NAME.
- With `HERDR_PANE_ID` set, Herdr's name for the pane decides, whatever `--role` says. A
  builder that passes `--role reviewer` is still the builder and is refused. A pane Herdr
  names nothing, a lookup that takes over 5 seconds, or any other Herdr failure refuses,
  and nothing is recorded or sent.
- With `HERDR_PANE_ID` set but Herdr's socket out of reach (connection refused, permission
  denied, socket missing), the claimed role decides, and the command prints a warning.

The last case exists for a sandboxed Codex reviewer. Codex runs its commands in a shared
app-server daemon, whose environment can carry another pane's `HERDR_PANE_ID` (F26 in
`guide/design.md`), and its sandbox denies the Herdr socket. An unsandboxed builder can
always reach the socket, so Herdr always names it.

**Every review starts in a fresh reviewer conversation, and a second look reads only the
change.** `tandem-review` resets the reviewer's conversation first (the same `tandem agent
reset` the build gets, under the same rules, with the reset command of the reviewer's
kind). `tandem gate scope` answers `diff` only when the earlier review was written by the
reviewer, under the same reviewer kind and model and the same builder kind and autonomy.
The reviewer then gets the earlier review and a `diff -U5` of the reviewed version against
the plan now, says for each earlier objection whether it is resolved (as `<review-id>#N`),
and checks only the changed text for the five defects. It does not re-read the plan or
re-run the check command. A plan more than about half rewritten, a changed reviewer or
builder, a review the builder wrote, or anything unexpected gets a full review. On
2026-09-26 the second round cost 2.5–2.8M input tokens per feature because it did neither.

**`tandem-handoff` refuses (exit 24) until a review clears.** The review is
evidence about one version of one plan, read by one reviewer, for one builder. Each
of these voids it, and the gate names it:

| Reason | Means |
| --- | --- |
| `no_reviewer` | no reviewer is recorded now. Checked first, whether or not a review exists |
| `plan` | the plan changed since it was reviewed |
| `builder_review` | the review was not written by the reviewer. Every review from before the reviewer was required counts as this |
| `reviewer` | the reviewer's kind changed, or its model changed when both sides recorded one |
| `builder` | the recorded builder kind or autonomy changed |

Reviewer effort and autonomy do not void a review. For every reason but `no_reviewer`,
`tandem-handoff` prints which it was and requests a fresh review itself. For
`no_reviewer` it names the two remedies above and sends nothing. A standing objection
is the one case it leaves to the planner: it can be overruled, but not silently.

```bash
tandem-handoff --override-review 20260910-204612-8821-19044 \
  --because "I commit in this repo, not you; skip the commit steps"
```

The reason is sent to the builder in the launch prompt, and it is the one
thing there that outranks the plan. It is recorded against the review, so the
ruling stands on every later handoff of that plan without retyping it — and
editing the plan voids the review and the ruling together. `--because` is
required with `--override-review`; naming a review that a newer one has since
superseded refuses (exit 24) without recording the reason anywhere, and names
the newer one to read instead.

### 4. Hand the plan over

```bash
tandem-handoff --dry-run    # renders the launch prompt without sending it
tandem-handoff              # sends it; defaults to the most recently reviewed plan
tandem-handoff docs/plans/2026-08-29-rate-limiting.md    # or name it explicitly
```

The builder gets **one task at a time**, each in a fresh conversation, and tandem sends
the next task itself. See [Per-task builds](#per-task-builds) below.

**Name the plan explicitly when it matters.** With no review on record, the default falls
back to the newest plan, and "newest" means newest mtime. Mtime is not authoring time — a fresh clone or checkout stamps every file at once. In one real repo, 20
of 55 plans shared an identical mtime, so "newest" was a 20-way tie broken by filename.

**The default path already verifies delivery.** After typing, `tandem agent prompt --as`
waits up to 8 seconds on Herdr's event stream for the builder to leave its settled status,
and `tandem-handoff` fails with **exit 7** if it never does. That is how a prompt
swallowed by an invisible native modal gets caught. You do not need `--wait` for that
assurance; `--wait` is for when you want to _block_ until the builder settles and see the
state it settled into.

**A turn that ends at once is not a delivery either.** Once the pane starts working,
`tandem agent prompt --as` watches it for 8 seconds more. A turn still running then
is delivered. A turn that settles sooner is judged by the conversation's own log: if its
token total grew, the model ran and the turn was just short (a Codex reviewer finished a
correct review in 5 seconds). If it did not grow, no model call was made, which is what a
model the CLI rejects, an expired login or used-up quota looks like. `tandem-review` and
`tandem-handoff` then exit 8 and print the pane's last 20 lines. A log that cannot be read
counts as delivered, with a note: wrongly blocking real work costs more than a late
warning, and a durable run that really did fail pauses later and the bridge says so. The
cost is that a successful send returns up to 8 seconds later.

#### A fresh conversation for the build

Before the reviewer pane was required, reviews and the build happened in the same builder
pane, so without intervention the build inherited every review round: on 2026-09-25 it
began with 141k tokens of context against 26k for the first review, and its 46 calls cost
7.8M input tokens, three-quarters of the builder's total. Reviews now run in the reviewer
pane, but the builder's conversation can still hold an earlier build or a `tandem-ask`
exchange, and none of that history is needed.

So just before sending the launch prompt, `tandem-handoff` runs `tandem agent reset`,
which types the builder CLI's own new-conversation command into its pane — `/new` for
Codex, `/clear` for Claude Code — and waits for the pane to settle. The pane, its process
and `builder.json` are untouched, so the review still stands.

**A reset has to show itself; waiting is not proof.** Claude Code reports a new session id
the moment it resets. For Codex, what shows the reset is the new conversation starting, a
lifecycle change Herdr counts in `state_change_seq` for that pane alone. The reset then
reports `sent`. For Claude, after delivery `tandem-handoff` asks `tandem agent session --not
<old id>` for the id the build actually runs in. If it never moved, you get a warning that
the build may be carrying the review, and nothing is recorded.

**tandem never reads a Codex pane's conversation from Herdr.** Codex runs every
conversation on one shared app-server daemon, which the first Codex TUI on the machine
starts and every later one joins. Herdr's Codex hook runs inside that daemon and reads the
pane id from its environment, so it credits every Codex conversation to the pane that
started the daemon, and never gives another Codex pane one. A daemon started outside Herdr
credits none (measured 2026-10-12, codex-cli 0.162.1). So a Codex pane with no session
in Herdr may hold any number of turns: it is reset like any other, never skipped as empty.
A Codex task's conversation is the one whose log holds the task's nonce, for its usage and
for judging a turn that ended at once. A legacy prompt carries no nonce, so for any pane
whose conversation Herdr does not name, `tandem agent prompt --as` adds one last line
to the prompt, `(tandem delivery <id>)`, and finds the turn's conversation by that.

**Codex 0.157 asks where the new conversation should run.** Its `/new` opens a native
picker ("Current checkout" or "New worktree") that Herdr cannot see: the pane stays `idle`
and nothing changes. So when a reset command changes nothing for two seconds, `tandem
agent reset` presses the builder kind's confirm key once (Enter for Codex, which picks the
highlighted "Current checkout") and waits again. If still nothing changes, the reset is
`unconfirmed`, and `tandem-handoff` and `tandem-review` send nothing and exit 7, naming
the pane. Typing into that screen is what lost a build prompt on 2026-09-28 (F21 in
`guide/design.md`).

It resets **only a conversation that does not hold a build task still in progress.** That
id is recorded in `.tandem-relay/build-session`; a later handoff into the same session
leaves it alone, because the planner's `tandem-ask` follow-ups rely on it. When a task is
checked and done, `tandem build advance` clears that record: the ledger now carries what
later tasks need, so the next task's handoff resets as usual.

**A Copilot builder is reset with `/new`, confirmed from Copilot's own log.** Herdr keeps
reporting the `--session-id` the pane was launched with after `/new`, and its lifecycle
counter does not move, so Herdr cannot show the reset. Copilot writes `Registering
foreground session: <id>` to its process log, `~/.copilot/logs/process-<ms>-<pid>.log`, the
moment `/new` runs. `tandem agent reset` finds the pane's Copilot process with `herdr pane
process-info`, and counts the reset only when that log names a conversation opened after
the command. Nothing else counts: a log line in any other shape leaves the reset
`unconfirmed`, and the task is not sent. For the same reason tandem never takes a Copilot
pane's conversation id from Herdr: a task's conversation is the one its reset named, or the
one whose prompt holds the task's nonce in `~/.copilot/session-state`.

**An opencode builder is not reset.** Its `/new` does empty the conversation, but nothing
shows it until the next prompt: Herdr never reports an opencode session, its counter does
not move, and opencode writes no session, log line or state file before that prompt. A
reset nobody can confirm is not claimed (F18 in `guide/design.md`). A reset that is skipped never
blocks a build: an unsupported kind, or a builder that is still working or at a dialog
before the reset, prints one warning and delivers into the existing conversation. Only a
reset that was typed and could not be confirmed stops the handoff, as above. A Claude or
Copilot builder with no session id yet has not taken a turn since `tandem-pair`, so there is
nothing to reset and nothing to warn about. A builder Herdr reports as `done` (a finished turn nobody has
looked at yet) counts as settled, exactly like `idle`: treating it as busy is what let a
build inherit its review on 2026-09-26 (F20 in `guide/design.md`). `--dry-run` reports what
it would do. `tandem-review` resets the reviewer pane the same way, under the same rules,
before every review.

#### Staffing advice (optional, off by default)

With `HERDR_DECISION=1` and a `TYPESAFE_API_KEY` in the environment, `tandem-handoff`
classifies the plan before sending it and adds a short, explicitly advisory block to the
builder's prompt: what shape the work is, whether it looks parallelisable, and — for
builders that can select a per-subagent capability — the lowest level to delegate it at.

The builder may ignore all of it. Nothing here can change a review verdict, widen
authority, alter a launch argument, or affect teardown. `internal/gate` is forbidden from
importing `internal/decision` by a test, so a judgement cannot reach a gate even by
accident.

```bash
HERDR_DECISION=1 tandem-handoff                   # ask for advice on this handoff
tandem decision assess --plan P --root R --dry-run  # show what would be sent, send nothing
```

Without the key, or if the call fails or times out, nothing is added and the prompt is
exactly what it would have been — `tests/test-decision.sh` asserts that byte-for-byte
against the real script. The credential is read from the environment only, never from
`.tandem.json`, which is cloned with the repository.

**The handoff says which it was.** Because every non-advice path leaves the prompt
untouched, "Jev was never asked" and "Jev answered and chose not to advise" look identical
from the prompt. So `tandem-handoff` prints one line either way, outside the prompt:

```
staffing advice: added to the launch prompt (Jev).
staffing advice: none (Jev answered but was not confident enough about this plan to advise).
staffing advice: off (HERDR_DECISION is not 1).
WARNING staffing advice: HERDR_DECISION=1 but TYPESAFE_API_KEY is not set; Jev was not asked.
WARNING staffing advice: the Jev call failed (<reason>); none added.
```

"None" is the common answer for a plan Jev cannot classify confidently: by design, an
uncertain assessment adds nothing rather than a guess. `tandem decision assess --status FILE`
is what reports it. Note the shell a handoff runs in: a planner started before you exported
`HERDR_DECISION` does not have it, and the line will say `off`.

Per-subagent capability selection is expressible by `claude` and `codex` builders.
`claude` agent definitions carry a default `model:` that the dispatch-time parameter
overrides. `codex` was verified against codex-cli 0.155.1 (`multi_agent_version` v2) by
reading a real session's rollout log: `spawn_agent` takes `model` and `reasoning_effort`
as well as `{task_name, fork_turns, message}`, and honours both — a parent running
`gpt-5.6-sol` at medium spawned children that ran `gpt-5.6-luna` at medium and
`gpt-5.6-terra` at high. Project-scoped `.codex/agents/*.toml` is still not read, so a
named persona cannot be selected this way, only a capability.

`copilot` and `opencode` are unverified, and unverified means "cannot express it" rather
than "probably fine" — for those the advisory still describes the shape of the work, it
just does not name a level.

> An earlier version of this section said the opposite of the `codex` paragraph above,
> citing the same CLI version. It was wrong when written rather than overtaken by a
> release. `internal/agent/subagent_test.go` now pins the claim to the evidence.

#### Per-task builds

One conversation holding every task was the largest cost left once reviews and builds got
fresh conversations. On 2026-09-28 an 8-task build read the whole 2,229-line plan, the
conventions file (which Codex had already loaded) and three plan-execution skills before
task 1, 64k tokens of context before any edit. Every task's reads then rode along into the
next: task 1 began at 93k, task 5 at 208k. The build spent 15.5M input tokens and stopped
in task 5 when Codex's 5-hour usage limit ran out. Replayed with each task starting fresh,
the same work costs about 4.4M.

So tandem runs the plan one task per conversation. The steps below are the **legacy**
engine, which is still the default. A **durable** run splits the plan the same way, but a
controller sends each task, runs the check and records the review, and the planner signs the
run off on its stored evidence rather than a report. See
[guide/build-engines.md](guide/build-engines.md) and [Durable state](#durable-state).

1. `tandem-handoff` splits the plan at its `Task N` headings (`tandem build start`). Each
   brief in `.tandem-relay/build/task-N.md` is the plan's title, its opening section (goal,
   architecture, constraints, minus any `REQUIRED SUB-SKILL` line) and one task. A plan with
   no task headings is one task. No model tokens are spent writing briefs.
2. The launch prompt names the task, its brief and a ledger of the tasks before it. It does
   not ask for the whole plan, and asks for the conventions file only when the builder's
   CLI does not load it itself (Codex loads `AGENTS.md`, Claude Code loads `CLAUDE.md`).
3. The builder does the task, runs the check through `tandem check`, and runs
   `tandem-handback --task N --report FILE` (with `--stuck` if it could not finish). That
   always goes through the mailbox to the relay bridge, never to the planner.
4. The bridge runs `tandem build advance`: it waits for the builder's turn to end, records
   the files the task changed, what its conversation and any subagents cost, runs the check
   itself outside the sandbox, and decides. A passing task sends the next with
   `tandem-handoff --continue`, which resets the builder and re-runs the review gate, so a
   plan edited mid-build stops the loop instead of building half of each version.
5. The planner is woken once, when the build finishes or stops, with one report in
   `.tandem-relay/reports/` that `tandem-unpair --verified` signs off as before.

Both engines need a reviewer pane. A legacy `tandem build start` refuses with exit 23 when
`reviewer.json` records none, and the build records that pane's name. `tandem-handoff`
refuses a durable start in the same case, naming the same remedy. Every legacy task gets
a review round in the reviewer pane, and the launch prompt tells the builder not to review
the task or spawn a reviewer subagent. Only [review triage](#review-triage-optional-off-by-default),
when it is on, can skip a round. When it skips one, nothing reviews that task.

**What stops the loop**, each with the remedy in the planner's message:

| Stop | Why | To carry on |
| --- | --- | --- |
| `stuck` | the builder said it could not finish | `tandem-ask builder "..."`, then it hands back task N again; `tandem-handoff` to resend task N fresh; or, if the task is done, `tandem build accept --task N --because "why"` and then `tandem-handoff` |
| `red` | the check tandem ran failed | the same. Handing back with nothing changed reruns the same check and stops again |
| `budget` | the task's input tokens passed `task_input_budget`. A reviewer's `changes` on a task over its budget stops here too, and no fix is sent | find out why (`tandem build status`), then `tandem-handoff` continues with the next task |
| `handoff` | the next task could not be sent (a changed plan, a dialog) | fix it, then `tandem-handoff` |
| `unsettled` | the builder never finished its turn after handing back | check its pane, then `tandem-handoff` |
| `review_edited_tree` | the reviewer pane changed the task's files or added one while it reviewed, so its review does not count | read `git diff`; `tandem build accept --task N --because "why"`, or undo the edits and `tandem-handoff` |
| `review_rounds` | the reviewer still asked for changes after 3 rounds | read its findings; `tandem-ask builder "..."` to fix and hand back again, or `tandem build accept --task N --because "why"` |

`task_input_budget` in `.tandem.json` defaults to 3,000,000 input tokens per task, and 0
turns it off. The most expensive task of that 8-task build, replayed fresh, came to 1.65M,
so the default leaves room for a big task and still stops a missed reset or a runaway task
long before a usage window is gone. It is measured from Codex's own session logs; other
builder kinds report "not measured" and are never stopped by it.

`tandem build status` prints the ledger: each task's status, check result, files, calls,
input and peak context. `tandem check [-- ARGS]` runs the check with its output in a log
under `.tandem-relay/build/checks/`, and prints the exit code, the lines that name a failure
and the last few lines. Two raw Jest runs put about 80k characters into one build's context
on 2026-09-28.

Measured live on 2026-09-28 (codex-cli 0.158.0, the eval fixture's 3-task plan): each
task's first call was 22.5k to 23k, the fresh-conversation floor; no task read the plan or
`AGENTS.md`; the bridge sent tasks 2 and 3 and finished without a human; the report passed
`tandem-unpair`'s gate. On a plan that small one conversation never grows, so splitting it
costs more (about 1.2M against 1.0M in one conversation, subagents aside): each task pays
the ~22k entry fee on every call. The split pays off where tasks carry real reads and test
output forward, as in the 8-task build above.

#### Waiting

The launch prompt tells the builder to wait for a subagent in a **single** call, using the
longest timeout its wait tool accepts.

That paragraph is there because the alternative is startlingly expensive. On one measured
pairing session, 48 of the builder's 173 API calls were orchestration — waiting and listing
agents. They consumed 4,544,623 input tokens and produced 2,285 output tokens between them,
roughly 48 tokens per call. The cause was a 60-second wait that expired: 18 of 46 waits
timed out, and a child taking three minutes forced three full-context round trips to sit
still. The floor is one wait per child, so about 26 calls were spent learning nothing.

The prompt names no tool and no number. One template drives `claude`, `codex`, `copilot` and
`opencode`, whose wait vocabularies differ, and a hardcoded value would rot the moment a CLI
renamed its parameter. It states the principle and the cost instead, which is what an agent
needs to pick a sensible value itself.

The same rule covers long commands. On a 2026-09-25 session the builder started the
project's ~20-second check with a one-second yield, then checked back 21 times — 3.0M input
tokens for 1.2k of output, each check-in a full-context call. The prompt now tells the
builder to run long commands, naming `tandem check` when the project has a check, in
one blocking call, and to raise *every* timeout its tooling has: under Codex's code mode a
command sits inside a second, outer yield, and raising only one still polls (F19 in
`guide/design.md`). The review prompt says the same about the one check it runs.

#### Review triage (optional, off by default)

Set `HERDR_JUDGE=1` when running `tandem-handoff` to enable task judgments.
Handoff stores the switch as `judge` in `.tandem-relay/build/build.json`; later
handoffs and the bridge use that stored value. Enabling it needs a
`TYPESAFE_API_KEY` from an active Jev subscription in Tandem's environment.
It works with every builder kind and leaves the builder's launch permissions unchanged.

Tandem decides when a task gets a review. The builder implements the task and hands
it back. `tandem build advance` runs the project's check, then asks Jev about the
files that task changed, including new files, with Tandem's actual check result.
The diff, brief and check output are redacted before the call, and the diff is
size-capped with truncation recorded. The builder needs no Jev network scope or
credential to perform the task.

During calibration, every judged task gets a review round, even when Jev returns
`clean`. The builder never reviews. `tandem build start` refuses with exit 23 when
`.tandem-relay/reviewer.json` records no reviewer pane, and the build records that
pane's name. The bridge types the review prompt into the reviewer pane. The reviewer
reads the brief and `git diff HEAD`, edits nothing, writes its findings and runs
`tandem-handback --task N --review --role NAME --verdict clean|changes --report FILE`,
where the prompt fills in NAME, the reviewer pane's recorded name. `tandem-handback`
refuses a review from any other pane, by the rule in
[Which pane is the reviewer](#which-pane-is-the-reviewer). `tandem build advance` refuses a review handback from any other pane,
or from a pane running the builder's conversation, and leaves the task in review. A
`clean` verdict accepts the task. With `changes`, the bridge sends the findings to the
builder's own conversation (`fix N MSGFILE`), the builder fixes them and hands back as
before, and the next round opens. After 3 rounds of `changes` the task stops with
`review_rounds`. A `changes` verdict on a task already over its budget stops with
`budget`. In both cases the findings are not sent. Tandem compares the tree before and
after each review: a reviewer that changed the task's files voids the round and stops
with `review_edited_tree`, and any change reruns the check.
A missing key, provider failure or malformed answer produces an `unavailable`
record with its reason and still gets a review. A delivery failure stops the task.

Each judgment is recorded to `.tandem-log/judge-<id>.json` with its answers, contract,
policy and model versions, the true check result, and whether it *would* have
advised skipping. Reviewer facts record whether the reviewer pane returned a
verdict, and whether the review changed the outcome: it asked for changes, or
edited the tree. These measurements provide the evidence needed before skips
can be enabled. A red check never qualifies for a skip.

Skipping also requires `HERDR_TRIAGE_SKIP=1` in the bridge's environment,
separately from `HERDR_JUDGE=1` at handoff. It is never read from `.tandem.json`.
`tandem triage report` must show at least 30 reviewed would-skip pairs, at least
10 pairs where the reviewer changed the tree, and zero misses under the current
contract, policy and model. One miss keeps that gate closed until a version changes.
Only a confident skip with a check that exited 0 and no stuck handback qualifies.
Confident means implements and evidence at least .90, scope creep and destructive
at most .10, and review priority at most .25. The diff must also be untruncated and
known to be the attempt's complete change. The current build loop reads a
HEAD-relative diff of a shared worktree, which cannot show that, so it still
records `would_skip` but never acts on it. Every row names exactly what it is
evidence about (`triage-evidence/v1`): a hash of the skip policy and its thresholds, a
hash of the judge's model and questions, a hash of the reviewer's kind, model and review
prompt, and the hash of the exact input the judge was shown. A change to any of them is a
new cohort: the report counts only rows of the current cohort and lists every other row
under the reason it was left out (`legacy_record`, `other_cohort`, `awaiting_review`,
`not_reviewed`, `judge_unavailable`, `incomplete_identity`, `tree_unknown`,
`tree_mismatch`, `truncated_input`). A pair needs the judgment and the review to be of
the same tree. A row that cannot be read, names another job than its file, or refers to
kept input that is missing or does not match its hash makes the evidence unhealthy and
closes the gate; it is counted, never dropped.
A deterministic one in five, selected by SHA-256 of the record ID and the policy hash,
read as a number, modulo 5, still gets a review to detect new misses; the choice is
settled when the judgment is recorded, before and independently of the verdict.
Skipped tasks retain null reviewer facts and never count as pairs. The report states an
upper bound on the true miss rate, and the rule-of-three caveat still applies: zero
misses in 30 allows a true miss rate near 10%, and reaching the minimum count is not a
proof of safety. Skip stays off by default whatever the report says; live calibration
and an explicit release decision are separate gates.

The exact input the judge was shown (the redacted brief, diff and check output, and the
versions it was shown under) is kept in `.tandem-log/judge-input-<hash>.json`, mode 0600,
named by its hash, so a judgment can be audited later. It is the text already sent to the
provider; keep `.tandem-log` out of version control.

With the stored switch off, Jev is never asked and the reviewer pane reviews every
task. Handoff prints whether Tandem calls Jev or why triage is off.
`tests/test-handoff-jev-status.sh` and `tests/test-handoff-reset.sh` cover those
instructions and their delivery.

### 5. The builder implements, and asks back

Mid-implementation Codex runs:

```bash
tandem-ask planner "should rate limiting be per-IP or per-API-key?"
```

Codex is sandboxed and cannot reach the Herdr socket, so this writes a question into
`.tandem-relay/q/`, `tandem-bridge` delivers it to the planner pane as a prompt, and the
planner's answer is written to `.tandem-relay/a/<id>.md` for Codex to poll. The answer is
**written as a file, never scraped from a terminal**, so nothing is lost to the terminal
collapsing long output.

### 6. Ask in the other direction too

```bash
tandem-ask builder "how far through the plan are you?"
```

Same command, but from the planner it goes straight over the socket. `tandem-ask` probes and
switches modes on its own — the CLI surface is identical either way.

**It blocks, and says so.** In mailbox mode it prints a progress line to stderr every 10s
(`HERDR_ASK_HEARTBEAT_SECONDS`), because a caller that sees nothing for minutes cannot tell
"still waiting" from "died" — and one that cannot introspect its own processes, as Codex
cannot under its sandbox, resolves that by asking again. Replies stay on stdout; progress
never mixes into them. A duplicate question is delivered for real, so the planner sees it
twice.

**The progress line also reports bridge liveness.** Each heartbeat checks whether
`tandem-bridge` itself is still running (a pid-file-plus-process-name check structurally
like the ones `tandem-unpair` and `tandem-status` already apply, extended to fall back to
the pid check alone if the process table itself is unreachable -- as it is under Codex's
own sandbox, the caller this check exists for), so a dead bridge and a busy peer no
longer look identical: a dead bridge is named explicitly, both at each heartbeat and in
the final timeout message, along with the fix -- re-run `tandem-pair` (idempotent, safe
to re-run on an already-paired session).

**Every completed exchange is durably recorded**, in either direction and either transport,
at `.tandem-log/<id>.json` (`id`, `ts`, `from`, `to`, `question`, `answer`) -- read it back
with `tandem-relaylog`. This is what a direct-mode builder otherwise leaves zero trace of,
and it survives `tandem-unpair`, which never touches this directory. A timeout or a failed
delivery leaves no record; only a question that actually got an answer does.

**The default timeout depends on the direction**, because one number cannot serve both.
Asking the **builder** waits 90s: the caller is normally Claude Code, whose Bash tool kills
a command at 120s, and a longer wait would be killed mid-flight — taking the graceful
"proceed and state your assumption" fallback with it. Asking the **planner** waits 300s:
the caller is the sandboxed builder, which has no such limit, and the planner may have to
read code before it can answer. A single 90s default once caused a builder to give up
mid-investigation and act on its own guess rather than the answer being written for it.
`--timeout` overrides both, and `HERDR_ASK_TIMEOUT_SECONDS` overrides the default.

### 7. The builder hands back, and the planner closes the session

For a build, the builder hands back each task with `tandem-handback --task N`, and tandem
writes the one report the planner receives when the build ends (see
[Per-task builds](#per-task-builds)). The rest of this section is about that report, and
about a plain handback, which a builder can still send at any time:

```bash
tandem-handback --plan docs/plans/2026-08-29-rate-limiting.md --report report.md
```

**The report is a file, and the planner is sent its path.** A completion report
is long, and terminal rendering collapses long output — the same reason answers
are files. It lands in `.tandem-relay/reports/<id>.md`.

**It does not wait for a verdict.** Verification is slow: reading a diff, running
a suite. A builder blocked on that would hit its timeout mid-review and report a
failure for a handback that actually arrived. So delivery is the whole job — the
builder goes idle and stays available.

**It does verify delivery, though.** `agent prompt` returning 0 is not proof: Herdr
cannot see native modals, and an agent at one reports `idle` while swallowing input.
So when the planner was idle beforehand, `tandem-handback` confirms it actually woke,
and exits **7** with the report path if it did not — the same guard `tandem-handoff`
applies in the other direction.

The planner wakes with the report path and two exits. If the work is wrong,
`tandem-ask builder "..."` sends it back while the builder still holds the
context. If it is right:

```bash
tandem-unpair --verified 20260829-141233-8821-19044    # the id is in the handback message
tandem-unpair --dry-run --verified <id>                # or see what that would do first
```

**Nothing closes the pane implicitly** — not the builder, not the bridge. Closing
a pane destroys its scrollback, so it is always a deliberate act.

**And "verified" is enforced rather than requested.** `idle` collapses three
different states — never started, handed back but unread, handed back and checked
— and only the last may be torn down. So `tandem-unpair` refuses unless:

| Gate | Refuses when | Exit |
| ---- | ------------ | ---- |
| not mid-write   | the builder is `working` or `blocked`      | 9 |
| work exists     | the builder has never handed back          | 4 |
| signed off      | a handback has no `--verified`             | 5 |
| check available | the repo has no `check_command`            | 6 |
| check passes    | `check_command` exits non-zero             | 7 |
| check still applies | a handback arrived, the tree changed, or the builder woke *while the check ran* | 8 |

"The tree changed" in gate 8 means the whole working tree by default. A repo can
scope it to just the paths its `check_command` actually exercises with
`check_paths` in `.tandem.json` (see "Per-project configuration" below), so an
unrelated concurrent change elsewhere in a large or monorepo checkout stops
causing a false "stale" refusal.

The first three are assertions the planner makes; the check is the only actual
evidence, which is why it runs here rather than being taken on trust — and it runs
**before** anything closes, so a red suite leaves the pair exactly as it was.

The last gate is what stops a *stale* green being reused. A real suite takes
minutes and the session is live throughout: the builder can hand back again,
resume work, or change the tree while the check runs. Any of those and the green
result describes something other than what is about to be closed, so
`tandem-unpair` snapshots the handbacks and the tree before the check and compares
after. Ignored files are excluded, so a test run's own build artefacts do not
trip it.
`--force` overrides every gate, `--skip-checks` only the last two, and
`--keep-relay` preserves the reports and their sign-off markers.

**A durable run is signed off on its own evidence.** It writes no handback report, so
gates 4 and 5 read the run from the state home instead of `reports/`. Gate 4 refuses when no
task was accepted. Gate 5 refuses, naming each task and what it lacks, unless every task is
accepted with a passed check and a recorded review for its final tree (or a waiver of
either, from `tandem build accept --waive`). `--verified` is not needed. Reports an earlier
legacy build left in the relay are listed as not part of the run and are not gated. The other
gates (not mid-write, the check, nothing moved while it ran) apply as before.

`--clean-trust` additionally removes the project-trust entry this harness added for the
repository. It removes **only** entries carrying tandem's marker — one you wrote
yourself, or one written before markers existed, is recognised and left alone — and it
skips entirely while another live agent of the same kind is still working in the
repository, since removing the entry would leave that agent stalling at a trust dialog.

An unsigned handback prints the exact command to run, and the rework command
beside it, because the point of the refusal is to make *send it back* as easy as
*close it*.

### 8. Sanity-check the loop on a new repo

```bash
herdr config check                 # validates config.toml
tandem-handoff --dry-run            # plan detection + prompt render should be instant
tandem-ask planner "test"           # from the builder pane: confirms the relay round-trips
```

If that second command is slow rather than instant, something in the prompt is executing
rather than rendering — see `guide/design.md`.

**`--dry-run` exists on `tandem-pair`, `tandem-handoff`, `tandem-ask`, `tandem-handback` and
`tandem-unpair`** — everything that touches another agent. `tandem-project` does not need it
(it only prints JSON) and `tandem-bridge` has `--once` instead. `tandem-init` has none, and it _does_ write: it creates
`.tandem.json` and appends to `.gitignore`. It will not overwrite an existing manifest
without `--force`, but read what it wrote afterwards.

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
[Per-task builds](#per-task-builds).

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

## How the relay works

**Only a sandboxed builder needs any of this.** A `claude`, `copilot` or `opencode` builder reaches
the Herdr socket directly, so `tandem-ask` takes the socket path and the mailbox below is
never touched. The bridge still starts. It sleeps on file and Herdr events with a 30-second
backstop (see [Commands](#commands)), so it costs little, and predicting that it is unneeded
is not worth a silent hang if the prediction is wrong. A durable run needs it whatever the
builder kind, because the bridge is what ticks the run.

For a Codex builder, which is the default:

Codex runs under a seatbelt sandbox that denies `~/.config/herdr/herdr.sock`, so every
Herdr command that talks to the server fails for it — which is all of them that matter
here. It can write inside its workspace. So:

```
Codex          <repo>/.tandem-relay/              Claude
tandem-ask ──►  q/<id>.json  ──►  tandem-bridge ──prompt──►  planner
     │                                                        │
     └──── poll ◄──  a/<id>.md  ◄──── writes the answer file ──┘
```

`tandem-ask` probes the socket and picks direct or mailbox mode automatically — the same
command works from either agent. `tandem-handback` and `tandem-verdict` do the same, but
nobody waits for an answer: the sender queues its finished message and exits, and the bridge
delivers it word for word. If that delivery fails, the bridge leaves a `<id>.UNDELIVERED`
marker beside what was sent — the report for a handback, the stored review for a verdict.

A handback travels the same way, with `"kind": "handback"` on the queued file. The
difference is that nothing is polling for a reply — the builder has finished — so the
bridge delivers the message its sender composed and leaves an `.UNDELIVERED` marker beside
the report if it cannot. The message text lives in `tandem-handback` alone, so the sandboxed
and direct paths cannot drift into saying different things.

Answers and reports are **written as files by their author**, never scraped from a pane.
Terminal rendering collapses long output (`… +N lines`), and a scraped answer loses the
collapsed part.

## Safety properties

Worth preserving if you edit these:

- **Never write raw bytes to a blocked pane.** That types into another agent's approval
  dialog. `tandem-ask` waits for the block to clear instead.
- **`agent prompt` returning 0 is not proof of delivery.** Herdr reports `idle` for an
  agent sitting at a native modal it cannot see, and silently swallows the input.
  `tandem-handoff` verifies the builder actually left `idle`.
- **Teardown is never implicit, and never unverified.** `herdr pane close` takes the
  pane's scrollback with it. Only `tandem-unpair` closes the builder, only the planner runs
  it, and it refuses unless the work was handed back and signed off (by report id for a
  legacy build, by each task's recorded check and review for a durable run), and the
  project's own check command exits 0, which it runs itself before closing anything.
- **The launch-prompt and handback templates are quoted heredocs filled by parameter substitution.**
  `prompt_extra` is arbitrary text from whatever repo you point this at; through an
  unquoted heredoc a backtick in a manifest would execute on your machine.
- **Unfaithful adapter mappings are refused, not approximated.** Widening grants more
  authority and requires user-only `allow_widen`; narrowing withholds requested authority
  and separately requires `allow_degrade`.

## Plans

Plans live in `docs/plans/` as one Markdown file each (`plans_dir` in `.tandem.json`).

```bash
tandem-handoff                          # most recently reviewed plan, else newest .md
tandem-handoff docs/plans/specific.md   # or name one
```

With no argument, `tandem-handoff`, `tandem-review` and `tandem-status` first ask `tandem
gate latest-plan`. It reads the newest review record in `.tandem-relay/reviews/` whose plan
file still exists, wherever that plan lives, so a plan reviewed outside `plans_dir` is
still found. With no such review, they pick by mtime, not by name, and do not recurse. That
fallback makes **every `.md` in that directory a candidate plan** — which is why the placeholder there
is `.gitkeep` and not `README.md`. Do not park notes, templates, or a directory README
alongside real plans; touching one of them makes it the next handoff.

A plan is read by an agent holding none of the conversation that produced it. State the
goal, the constraints, and what "done" looks like — and say explicitly what it must not
touch, because it cannot infer scope from context it never had.

### What review will reject

A plan is reviewed before it is built (see [step 3](#3-the-reviewer-attacks-the-plan)), and
the reviewer is looking for five things. Writing with them in mind is cheaper than
answering them later:

1. **Ordering** — a task requiring something a later task introduces.
2. **Contradiction** — two tasks expecting incompatible things of one file,
   command output, or state.
3. **Impossible step** — a step that cannot execute as written. A task that must end with
   the check failing counts: tandem checks every task and stops the build on red, so write
   failing tests in the same task as the code that makes them pass.
4. **Infeasible for the builder** — a step needing authority the builder does not have.
   Commits are the usual one: check whether the builder in this repo commits
   at all before writing a task that ends in one. The review prompt names the
   builder's kind and autonomy from `builder.json`. The reviewer runs in its own
   sandbox, so a probe that sandbox refuses proves nothing about the builder, and
   the reviewer reports it as "could not verify", not as an objection.
5. **Plan versus its own evidence** — a supplied test or example that would not
   produce the result the plan claims.

## Tests

```bash
./tests/run.sh
./tests/run.sh --with-clis   # additionally probe installed agent CLI flags
```

Shell-syntax checks over `bin/`, then `go build ./...` and `go test ./...`, then every
`tests/test-*.sh`. Exit 0 means the harness is sound. This is also the `check_command` in `.tandem.json`, so `tandem-unpair` runs it
before it will close a builder pane.

That last point constrains what a test here may do. **`tandem-unpair` runs the check while
the builder is still live**, so a test that wanted a free pane, a running Herdr server, or
sole ownership of `~/.codex/config.toml` would deadlock every teardown — it would fail
precisely because the thing it is gating on has not been torn down yet. Tests are
therefore hermetic: `herdr` is stubbed onto `PATH` and `CODEX_HOME` is redirected into a
temp dir, so they run with no server, no panes, no network, and no writes outside
`mktemp` or the repo. The one in-repo exception is `.gocache/`, where `GOCACHE` and
`GOMODCACHE` are pinned: the builder's sandbox denies `~/Library/Caches/go-build`
outright, and a cache there would break the rule above anyway. It is gitignored, so it
cannot trip the tree-change gate.

`test-relay-ignore.sh` lifts `ensure_relay_ignored()` out of `tandem-pair` with `sed` and
runs that, rather than keeping its own copy of the logic — a test asserting against a
duplicate of the code it is testing proves only that the duplicate is self-consistent.

`test-staleness.sh` asserts the installed `tandem` matches the Go sources, which is what
makes the copied binary safe.

**Run `./install.sh` from the branch under test before any live check.** The `bin/`
commands are symlinks into the checkout, so they are always current, but `tandem` itself
is a compiled copy and stays at whatever was last installed. A live pair run against a
stale binary tests old Go code behind new scripts. `tandem version` prints the first 12
characters of the source hash; `bin/lib/source-hash.sh .` prints the whole of the one it
should match.

`test-dispatch.sh` covers `tandem <verb>`, and is bash because it cannot be Go: dispatch
ends in `execve`, so a Go test calling it would never return to assert anything. It builds
its own binary, invents a delegate that exits with whatever it is told, and checks each
code in the vocabulary above arrives unchanged — a wrapper that observed a child and
re-exited would flatten 23 to 1 and turn a gate refusal into an ordinary error. It also
pins that a compiled subcommand is never shadowed by a stray `tandem-version` on `PATH`.

`test-cli-drift.sh` is opt-in through `--with-clis`. It probes flags on installed agent
CLIs, but stays out of the default teardown check so machines without those CLIs remain
supported.

**Measuring token use.** `tandem usage ROLLOUT.jsonl...` reads Codex session logs and
prints, per phase (one per prompt handed over): calls, the context size of the first call,
peak, input, cached and output tokens, `write_stdin` polls, and the `yield_time_ms` values
requested. Pass the review session and the build session together — the build now runs in
its own. Logs live under `~/.codex/sessions/YYYY/MM/DD/`. Codex only; a durable run's
`tandem build usage` also reads Claude and Copilot builders.

`./tests/run.sh` unsets `HERDR_DECISION`, `HERDR_JUDGE` and `TYPESAFE_API_KEY` first: a
machine that turned Jev on in its shell profile must not change what the suites see, since
`tandem-unpair` runs the same check in that environment.

The Go half carries the decisions. `internal/gate` holds the teardown verdicts as
pure functions — codes 4, 5, 6, 8 and 9; exit 7 is the project's own check command
failing, which bash runs and reports — and is forbidden — transitively, checked with `go list -deps` — from
reaching `os/exec` or the herdr facade, so the gates can be tested with no terminal, no
server and no filesystem. `internal/hooks` holds the Codex hook-trust classification;
it replaced a 20-assertion bash suite that lifted `codex_hook_trust()` the same way
`test-relay-ignore.sh` does, and carries the same 20 assertions as `t.Run` subtests,
plus one for a machine still holding a pre-rename baseline.

## Layout

```
bin/          the twelve commands — bash, symlinked by install.sh, live-editable
cmd/tandem/   the compiled core's entry points: its own subcommands, the handover to
              the bash ones (dispatch.go), and exit codes
internal/
  agent/      the adapter seam: normalised vocabulary -> each CLI's native flags. Pure;
              a dependency-closure test forbids exec, net and herdr in its reach
  agent/trust/  launch-time gates that touch another tool's own config — the I/O half,
              kept out of agent/ so that purity test stays meaningful
  config/     repo and user config, precedence with provenance, kind approvals
  herdr/      execs the herdr CLI for actions, and reads the socket's event stream for
              every wait on an agent's status, session or departure
  mailwatch/  wakes a waiter when a message lands in a relay queue or a named file
              changes (fsnotify); a wake is a hint, the caller still lists the folder
  gate/       the teardown verdicts, pure — no exec, no herdr, no terminal
  hooks/      Codex hook-trust classification, pure
  mailbox/    handback report snapshots; reads files, never shells out
  vcs/        `git status --porcelain`, and the per-file hashes that tell which files a
              build task changed
  plan/       splits a plan into one brief per task, pure
  build/      the legacy engine: the per-task build record, the decision after each
              task, and the ledger, report and planner message rendered from it
  check/      runs the project's check with output to a log, and summarises it
  paths/      where tandem's own files live, and the one place that knows their
              pre-rename names. `tandem paths` exposes it so bash shares the rule
  exitcode/   the shared exit-code vocabulary
  rollout/    Codex rollout-log parsing for `tandem usage`, pure
  sourcehash/ the source fingerprint install.sh stamps into the binary
  decision/   staffing advice from Jev, pure; evidence/ builds its redacted input
  triage/     whether a finished task still needs a review (Jev), off by default
  # the durable engine, see guide/build-engines.md
  controller/ start, tick, recover and stop; runs the check and binds it to a tree
  workflow/   the run's pure records, transitions and errors; imports nothing else
  state/      the versioned SQLite store per checkout, leases and fences (pure-Go driver)
  runner/     delivers the controller's prompts under a fenced lease, and says honestly
              whether each was sent, not sent, or may have been sent
  snapshot/   captures the tree at a moment; a task's patch is the diff of two captures
  artifact/   content-addressed bytes in the state home, outside the repository
  taskcontext/ the one bounded prompt a task gets, and a manifest of what it left out
  memory/     which remembered decisions a later task may be shown, and why
  usageledger/ normalises provider logs into per-event usage for collaborations
  evalrow/    the result row a scripted scenario run writes, legacy and durable alike
tests/        bash suites; also runs go build and go test. `--with-clis` adds a probe of
              the installed agent CLIs for flag drift, kept out of the default run
              because check_command must pass on a machine with none of them
tests/lib/    stubs shared by the handoff suites
guide/        design notes, the build engines, operations, versioning and releasing
tools/        maintainer scripts: package-release.sh builds the release tarballs
```

The split follows one rule: **Go decides, bash explains.** Go returns a verdict and at
most a machine-readable token; every word a human reads stays in the bash script that
prints it. `guide/design.md` says why. The exceptions are sentences two callers must say
identically: the reset sentences (`tandem agent explain-reset`), and the build's report and
planner message, which `tandem build` writes because the bridge and the tests must agree on
every word.

## Design notes

`guide/design.md` explains why the harness is shaped this way — the sandbox constraint that
forces the mailbox, why answers are files rather than scraped panes, the invariants that
keep it safe to edit, and the twenty-eight findings (plus four wrong predictions) behind them.
Read it before changing the relay or the launch prompt.

## Licence

MIT — see [LICENSE](LICENSE).

The `tandem` binary links Go modules under MIT and BSD-3-Clause licenses, and
SQLite, which is public domain. Each release tarball carries their notices in
`THIRD_PARTY_NOTICES`, generated from the binary at packaging time.

## Durable state

What separates the legacy and durable build engines is in
[guide/build-engines.md](guide/build-engines.md). The operator's guide is
[guide/durable-runner-operations.md](guide/durable-runner-operations.md);
the current rollout decision and its evidence are in
[guide/durable-runner-rollout-report.md](guide/durable-runner-rollout-report.md). Durable runs
stay opt-in.

`tandem state` looks at the durable store, preserves legacy history and moves it.
`status` and `import-legacy --archive` never switch the build engine. `import-legacy --active`
does, for the one checkout it names. Durable runs stay opt-in.

    tandem state status --json                    # one JSON object; reads, never writes
    tandem state import-legacy --archive PATH     # keep a finished build as archived history
    tandem state import-legacy --active DIR --quiescent
                                                  # carry a stopped legacy build into a durable run
    tandem state export --run ID --output PATH    # copy a run and every artifact it names
    tandem state recover --run ID                 # reconcile a run, report where it is
    tandem state rebind --checkout ID --root PATH # point a checkout's state at a moved worktree
    tandem state gc [--apply]                     # report unreferenced artifacts; --apply deletes them
    tandem state archive --run ID                 # make a settled run history, keep all of it
    tandem state engine                           # which engine owns this relay; refuses an unknown one

`status` reports whether a store exists, its schema version, the current run and why it is
paused, and how complete the recorded token usage is. A store written by a newer tandem is
reported by version only and exits 23. `import-legacy --archive` takes an archive made by
`tandem build archive` (which `tandem-unpair`, a replacing handoff and a durable start over a
finished legacy build run for you), verifies
every byte, stores each file as an artifact and records one archived run. Importing the same
content again, from any path, returns the same run. It keeps what the legacy build recorded
and marks what it never did (snapshots, check certificates, review results) as unknown;
imported tasks are never counted as accepted.

`import-legacy --active DIR --quiescent` (DIR is any directory in the checkout) moves a
**stopped** legacy build onto the durable engine. `--quiescent` is your statement that
nothing is writing, and tandem checks it anyway. It refuses, writing nothing, if the build is
running or finished, its record changes while it looks, the builder's pane is not idle or
done (or cannot be seen), the plan is not the one the build started from, or the checkout
has a durable run or a writer lease, even an expired one. Otherwise it archives the build as
a history run, starts a new run over the whole plan, and sets the legacy `build.json` aside as
`build.json.imported`. Tasks the legacy engine finished are carried with one accepted attempt
each. That attempt has no snapshot, patch, check or review, because the legacy engine never
recorded them in a form that can be checked, and its check and review stay pending. The first
unfinished task starts as a new attempt from a snapshot of the tree as it is now. The result
lists what stayed unresolved.

`export` writes a new directory (it never overwrites one) holding `manifest.json`, every
artifact the run's rows name, the files of its snapshots and the sides of its patches, in the
layout of an artifact store. Each copy is verified, and `COMPLETE` is written last. A delivery
whose outcome is unknown is named in `unresolved`. To roll back from the durable engine: stop
the run, let its jobs settle, export it, keep the durable store, and start a separate legacy
run if you need one. A durable run is never reinterpreted as a legacy one.

`recover` reconciles the run's pending and uncertain effects, ends any builder job whose attempt is already accepted or replaced (and, on a stopped run, every builder job still open), and prints its state. A pause
that needs a person is a successful result with `state` paused and a `pause_code`. `rebind`
needs the checkout idle (no running or stopping run, no held lease, no open job or delivery)
and the new path in the same git directory; a repository that itself moved needs
`--repository-moved`, and only works once the old git directory is gone. A refusal that needs
a person exits 23 with the reason on stderr.

`gc` prints one JSON object: `candidates` (artifacts no row, reference or snapshot names),
`deleted`, `retained`, and `missing` and `corrupt` for referenced artifacts that are gone or
fail their hash. Those two are never candidates, since they are evidence. Without `--apply`
it only reads. `--apply` needs an idle checkout (no running or stopping run, no writer
lease, expired or not), takes the exclusive operation lock (refused while anything is
publishing), works out the references again after taking it, and deletes a candidate's row
before its file. It never deletes by age and never deletes a run. `archive` refuses while a
run is running or stopping or has an open job or delivery, and sets the relay's marker aside
as `active-run.<run id>.archived.json`.

`tandem-unpair` on a durable run signs it off on the run's own evidence (see
[step 7](#7-the-builder-hands-back-and-the-planner-closes-the-session)), stops it, waits for it to settle (`TANDEM_UNPAIR_SETTLE_POLLS`
one-second polls, default 10), archives it and then removes the pane, the bridge and the relay.
The run's rows and artifacts stay in the state home. If the run does not settle, or an archive
is refused, nothing is closed or deleted; `--force` closes anyway and keeps the state.
`tandem-bridge`, `tandem-handoff` and `tandem-unpair` first run `tandem state engine`. A relay
whose `active-run.json` names an engine or protocol other than 2, a marker that cannot be
read, or a marker beside a legacy build is refused with exit 23 before anything is read from
or written to it. The remedy is to update the install with `install.sh`.

## Requirements

Herdr 0.9.3+ (every wait reads the event stream whose versioned schema 0.9.3 introduced,
protocol 22; F27 in `guide/design.md` pins the shapes), `jq`, `git`, `bash`, `curl`, `tar`,
`shasum` or `sha256sum`, and the Herdr integration for each agent you use:
`herdr integration install claude` (always required, since the planner is Claude Code), plus
`codex`, `copilot` and/or `opencode` for whichever builder you pick.
`herdr integration install` lists every kind Herdr supports.

The agent CLIs are runtime dependencies only for the kinds you actually use; nothing here
requires all four.

Go 1.26+ is needed only to build from source (`./install.sh --from-source`) and to run
`tests/run.sh`. A release is built with `CGO_ENABLED=0` for macOS and Linux on arm64 and
amd64, which works because the durable state store uses the pure-Go `modernc.org/sqlite`
driver. There is deliberately no bash fallback for the binary: two implementations of a
teardown gate is worse than one, because a diverging second one fails by passing when it
should refuse. `jq` is still required; this work traded silent failure for loud failure in
the places that matter, it did not remove the dependency.

Developed and exercised on macOS only. `tandem-handoff` probes for BSD vs GNU `stat`, so
plan selection should work on Linux, but nothing here has been run there.

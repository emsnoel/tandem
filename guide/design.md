# Design notes

Why this harness is shaped the way it is. Most of what follows was learned by running it
against a live Herdr server rather than by reading docs, and several of the decisions look
arbitrary until you know what they are avoiding.

The early findings were established against **Herdr 0.8.2** with **Codex CLI 0.150.1** and
**Claude Code** on macOS. Later ones name their own versions; the event stream tandem now
waits on is Herdr 0.9.3's (F27). Where a claim is version-specific it says so.

---

## The loop this supports

1. The **planner** (Claude Code) brainstorms, writes a spec, writes an implementation plan.
   The **reviewer**, a separate pane, reviews the plan before the build. The builder never
   reviews.
2. The **builder** (Codex) executes that plan.
3. The builder asks the planner clarifying questions mid-implementation.
4. The builder hands the finished work back with a report.
5. The planner verifies it against the tree — and either sends it back to step 2, or
   closes the session.

Without tooling, a human is the transport for steps 2–5: pasting plan paths, noticing when
the builder has stopped, relaying questions. That role is what this removes.

**Step 4 was missing from the first version of this harness, and its absence was not
obvious.** The launch prompt told the builder to "report" when it finished, which it
faithfully did — into its own pane, which nothing reads. Every other stage had a
destination; that one had an audience of nobody. The failure mode is quiet: no error, no
timeout, just a session that stops making progress at the exact moment the work is done,
until a human happens to look at the right pane. A loop that automates the handoff but not
the handback still needs a human watching it, which is most of what it was meant to avoid.

## Why the handback does not wait for a verdict

`tandem-ask` blocks: the caller needs the answer to carry on. The obvious symmetry is for
`tandem-handback` to block too, and take the planner's accept-or-rework as its return value.

It should not, because the two calls are waiting on different things. A question is
answered from what the planner already knows; verification is not. Checking a handback
means reading a diff and running a suite, which takes minutes, not seconds — well past
`tandem-ask`'s 90s bound, and that bound cannot simply be raised, because it exists to stop
a wedged peer from hanging the caller forever. A blocking handback would therefore usually
time out and report failure for a message that was delivered and is being acted on, which
is the worst of both: the builder believes it failed, the planner believes it is done.

So delivery is the whole job. The builder goes idle holding its context, and rework goes
back over the channel that already exists.

## Why teardown is gated, not just deliberate

Making teardown a separate command stops it happening *by accident*. It does nothing about
it happening *carelessly*, and the first version shipped exactly that hole: `tandem-unpair`
checked only that the builder was not mid-write. A builder that had crashed, or had never
been given a plan, was `idle` and therefore closable. So was one whose report read "Task 3
INCOMPLETE, 4 FAILING" — the pane closed and the report was deleted in the same run.

`idle` collapses three states that are not the same thing:

| State | `agent_status` | Safe to close |
| ----- | -------------- | ------------- |
| never started, or crashed | `idle` | no — its pane is the only record of why |
| handed back, unread       | `idle` | no |
| handed back, verified     | `idle` | yes |

The planner is also the wrong agent to trust with an unenforced "please check first". It
wrote the plan, so it is judging its own design, and it is the party that wants the session
finished. An instruction in a prompt is not a control.

So three gates stand before the close. Two of them — a handback exists, and it is named
with `--verified <id>` — are assertions, and an agent can make a false assertion. Their
value is that sign-off becomes an explicit act about a *specific* piece of work rather than
a reflex, and a second handback arriving during review forces a fresh decision rather than
riding along on the first.

The third gate is different in kind: `check_command` is run by `tandem-unpair` itself, and a
non-zero exit refuses the teardown. It is the only step in the sequence that produces
evidence rather than a claim, which is why it is not optional and why a repo with no
`check_command` is told to say so out loud (`--skip-checks`) rather than being quietly
waved through. It runs before anything is closed, so a refusal costs nothing.

Sign-off deliberately does not survive a refusal. If the check rejected the work, the
builder has changed something since, so the earlier sign-off described a tree that no longer
exists.

### A check is evidence only about the state it ran against

A green result is not a property of the session, it is a property of one moment. A real
suite takes minutes, and nothing pauses while it runs: the builder can hand back again,
wake up and start writing, or change the tree underneath it. Reusing that green afterwards
is the same mistake as trusting the report — believing a claim about a state that has since
moved.

So the handbacks and the working tree are snapshotted before the check and compared after,
and a difference refuses the teardown with exit 8. The rule stated positively: **every
handback being signed off must pre-date the check that verified it.**

The working-tree half of that snapshot is git's own `status --porcelain`, scoped to
`check_paths` when a repo declares one (`.tandem.json`, optional — see "Per-project
configuration" in guide/configuration.md) and to the whole tree otherwise. Scoping only ever narrows
what counts as a change: a repo that declares `check_paths` is trading completeness for
accuracy on purpose, on the assumption that `check_command` only ever exercises those
paths — an edit outside them during the check is no longer evidence the check's result is
stale. Get the list wrong (too narrow) and a genuinely stale green can slip through; the
default (unset) is the original whole-tree behavior, so nothing changes for a repo that
never declares it.

It is a snapshot rather than a comparison against the check's start time, and that matters.
`date +%s` and stat(1) mtimes are both whole seconds, so a report landing in the same second
the check began compares equal, not greater, and slips through a `>` test. The snapshot has
no such window: a file that was not there before is caught whenever it appeared. The first
implementation had the race, and a test that wrote a report and started a check inside one
second was what found it.

The tree fingerprint is `git status --porcelain` output compared as text, not a hash of it.
Hashing bought nothing and put the gate behind `shasum`, which is not present everywhere —
and a missing hasher would have produced an empty fingerprint and disabled the comparison
silently. A check that fails open is worse than no check, because it reads as one.

## Why teardown is a separate command

`herdr pane close` destroys the pane's scrollback. That makes it the one irreversible act
in the harness, and it is deliberately not attached to anything that happens automatically
— not to a successful handback, not to the bridge noticing the builder is idle.

`tandem-unpair` refuses while the builder is `working` or `blocked`, because a pane closed
mid-write takes the unfinished work with it, and a blocked agent may be one keypress from
writing. It resolves the mailbox path from the builder's own cwd **before** closing
anything, since that cwd stops being readable the moment the pane goes. And its `rm -rf`
is built from a path read over a socket, so it checks the basename is `.tandem-relay`
before removing it rather than trusting what it was handed.

**It closes the pane before stopping the bridge**, which looks like the wrong order and is
not. Stopping the bridge first and then failing to close leaves a live builder whose relay
has been killed underneath it: its questions stop arriving, with no error on either side. A
bridge that briefly outlives the pane has nothing left to deliver and costs nothing.

**It confirms the pid is a `tandem-bridge` before signalling it.** A pid file outlives the
process that wrote it and the OS reuses pids, so `bridge.pid` is a claim about a process,
not proof of one — and the consequence of believing it is killing a stranger's process.

Its counts come from globs rather than `find ... | wc -l`. Under `set -o pipefail` a `find`
on a missing directory exits non-zero, which aborted the whole command before it closed
anything — and `tandem-pair` creates only `q/` and `a/`, so `reports/` is missing in every
session that never handed back. It failed silently, in the common case, at the one job it
had.

## Architecture

```
workspace
└── tab
    ├── pane  ── agent name: planner  (claude)  role token: PLAN
    ├── pane  ── agent name: builder  (codex)   role token: BUILD
    └── pane  ── agent name: reviewer (any)     "reviewer" block, or tandem-pair --reviewer KIND
```

The reviewer pane is required. Every plan review and every task review, on both build
engines, goes there, on its own model, so the agent that wrote the code does not review it.
Without it, review and build refuse and name the remedy.

Names are **role-based, not tool-based**, so the harness survives swapping which model
fills which seat. They satisfy Herdr's constraint `[a-z][a-z0-9_-]{0,31}`.

**One shared working tree, not a worktree per agent.** Worktrees give real isolation and
were the first instinct, but they duplicate whatever the project's dependency tree is —
`node_modules`, Pods, Gradle state, a target directory — and need their own build before
anything can be verified. For a project whose verification step is expensive, that cost
lands on every handoff. Herdr supports worktrees (`herdr worktree`) if a project suits.

**Naming is load-bearing.** `herdr agent prompt <target>` accepts a unique live agent name
or a pane ID. Agents launched by typing `claude` or `codex` into a shell are detected but
**unnamed**, so every call would need a pane ID that changes between sessions. `tandem-pair`
exists mostly to assign those names.

## Why there is a mailbox

The obvious design is agent-to-agent over the Herdr CLI. It does not work in one direction.

**Codex runs under a seatbelt sandbox that denies the Herdr control socket.** The socket
lives at `~/.config/herdr/herdr.sock`, outside the workspace, and the default
`workspace-write` policy blocks it. Tested:

| Codex sandbox policy                            | Socket                                      |
| ----------------------------------------------- | ------------------------------------------- |
| default `workspace-write`                       | **blocked** — `PermissionDenied`            |
| `sandbox_permissions=["disk-full-read-access"]` | **blocked** — connect needs write, not read |
| `sandbox_mode="danger-full-access"`             | works                                       |

There is no targeted permission that exposes only the socket. Making the direct relay work
means disabling the sandbox entirely for an agent whose job is autonomously executing
plans — a bad trade.

Codex _can_ write inside its workspace. So:

```
builder (sandboxed)      <repo>/.tandem-relay/          planner (unsandboxed)
  tandem-ask ──write──►  q/<id>.json  ──►  tandem-bridge ──prompt──►  planner
       │                                                                │
       └────────poll ◄──  a/<id>.md  ◄──────── writes the file ─────────┘
```

`tandem-ask` probes the socket and picks direct or mailbox mode automatically, so the same
command works from either side and neither agent needs to know it is sandboxed.

The planner→builder direction needs none of this: the planner is unsandboxed and prompts
the builder directly.

### Answers are files, never scraped panes

The responder writes its answer to a path named in the message. The earlier design scraped
the responder's pane with `agent read`, which fails two ways:

- **Terminal rendering collapses long output** — Claude Code shows
  `… +14 lines (ctrl+o to expand)` and the collapsed text is simply not in the buffer. A
  long answer arrives truncated with no indication anything was lost.
- **`agent read` returns plain text, not JSON.** Every other command
  (`agent list`, `agent get`, `pane layout`, `pane list`, `workspace list`) returns a JSON
  envelope; `agent read` and `pane read` do not. Parsing them with
  `jq -r '.result.read.text'` silently yields empty. The socket _API_ schema does describe
  a `PaneReadResult.text` — the CLI unwraps it. Reading the schema is not the same as
  running the command. The event stream does match the schema's shapes, apart from the
  two spellings of event names in F27.

Writing a file sidesteps both.

---

## Invariants worth preserving

If you edit these scripts, these are the non-obvious properties keeping them safe.

**Never write raw bytes to a blocked pane.** Herdr rejects `agent prompt` against an agent
sitting at an approval dialog _before writing anything_, which is a feature: a relayed
question can never be swallowed by someone else's permission prompt. The tempting
workaround — `pane send-text` to push past it — types into that dialog and can approve
something on the user's behalf. `tandem-ask` waits for the block to clear instead.

**`agent prompt` returning 0 is not proof of delivery.** Herdr cannot see _native_ modals.
An agent sitting at one reports `agent_status: idle` and `interactive_ready: true` while
swallowing every keystroke sent to it. Observed with Codex's first-run "6 hooks need
review" dialog: the prompt returned exit 0, the handoff reported success, and the plan was
silently lost. `tandem-handoff` therefore verifies the builder actually leaves `idle` and
fails with exit 7 if it does not.

**Closing a pane is the one irreversible act, so nothing does it automatically.**
`herdr pane close` takes the scrollback with it. Only `tandem-unpair` closes the builder,
only the planner runs it, and it refuses while the builder is `working` or `blocked`.
Resist wiring it to a successful handback: an agent that can end the session it is being
judged in has an obvious incentive not to look too hard first.

**The launch-prompt and handback templates are quoted heredocs filled by parameter substitution.**
It must interpolate a plan path, so an unquoted heredoc is the obvious approach — and it
makes backticks and `$(…)` in the prompt body execute. `prompt_extra` is arbitrary text
from whatever repo the harness is pointed at, so an unquoted heredoc would let a backtick
in someone's `.tandem.json` run a command on the planner's machine. This is not theoretical:
during development `tandem-handoff --dry-run` executed a shell command while merely
_rendering_ text. Bash `${var//pattern/replacement}` never re-evaluates the replacement.
`tandem-handback` follows the same pattern: it interpolates a plan path and a report path,
and both come from the builder.

**A prompt that renders a command must quote every path it interpolates.** Prose can hold a
bare path; a command line the agent will copy cannot. `tandem-handoff` renders
`tandem-handback --plan "{{PLAN}}"`, and without those quotes any repo with a space in its
path produced `unknown argument` at the final step — reintroducing the stall the handback
exists to prevent, in the one place nobody would look for it.

**macOS `bash` is 3.2, where an empty array under `set -u` is a fatal error.** Both
`"${arr[@]}"` on an empty array and a `find` that exits non-zero inside a `set -o pipefail`
pipeline abort the script mid-run — and in the observed cases the script still exited 0, so
the caller saw success while nothing had happened. Both bugs landed in `tandem-unpair`, both
were silent, and both broke it in its most common state. Iterate with
`${arr+"${arr[@]}"}`, count with globs rather than `find | wc -l`, and treat any early
`set -e` exit in these scripts as a correctness bug rather than a style issue.

**Approvals off is not the sandbox off.** The builder launches with
`--ask-for-approval never --sandbox workspace-write`. Those two flags look like they do the
same job and do not: the first stops Codex pausing for a human who is not there, the second
is what the entire mailbox is built on. An out-of-workspace write must keep failing as
`operation not permitted` — an error the model reads and routes around — rather than
becoming a dialog nobody answers. If a sandbox denial ever looks like the bug, the fix is
never to widen `--sandbox`; that hands socket access to an agent executing plans
autonomously and quietly retires the relay.

**Codex's trust prompts are native modals, so they must be cleared before launch, not after.**
Directory trust is recorded in `~/.codex/config.toml` by `tandem-pair` ahead of
`agent start`. There is no reactive fix available: by F12 the pane reports `idle` and
`interactive_ready: true` while the modal eats every keystroke, so nothing downstream can
detect the stall, and `pane send-text` to dismiss it would be approving a trust decision on
the user's behalf.

**One resolver decides where the mailbox is, because disagreeing about it is silent.**
The mailbox, the repo manifest and the Codex hook baseline were all renamed off the
`herdr-` prefix, and all three fall back to the old name when only that is present. If the
bridge and the asking agent ever computed that fallback separately and differed, one would
watch a directory the other never wrote to: no error, no log, questions simply unanswered
until someone noticed the session had gone quiet. So `internal/paths` holds the rule once
and the scripts ask it through `tandem paths` rather than restating it. The same applies to
what gets gitignored — ignoring the new name in a repo still using the old one leaves live
relay traffic in `git status`.

**Lifecycle state is a hint, not a gate.** `idle` can mean a hidden modal; `blocked` is
usually transient (a permission dialog that clears in seconds) and should be waited out,
not treated as fatal. Every automated delivery needs its own confirmation.

---

## Findings

Numbered as originally recorded. All were observed against a live Herdr server except
F10, which is a platform difference that could not be reproduced on this macOS host — it
was verified instead by shimming a GNU-behaving `stat` onto `PATH`.

| #   | Sev      | Finding                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                           |
| --- | -------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| F1  | Critical | `agent read` / `pane read` print plain text, not JSON. Parsing as JSON discarded every reply — the relay never returned an answer. Proven: 5703 bytes discarded, 0 returned                                                                                                                                                                                                                                                                                                                                                                       |
| F2  | High     | The planner reports `blocked` while at a **tool-permission dialog** (measured `blocked` → `working` over 6s). Refusing on `blocked` meant the relay failed exactly when the planner was waiting on a permission prompt. Now waits it out                                                                                                                                                                                                                                                                                                          |
| F3  | High     | `tandem-handoff` told the builder to run `tandem-ask <planner>` without checking a planner existed                                                                                                                                                                                                                                                                                                                                                                                                                                                  |
| F4  | Medium   | **Herdr rewrites `config.toml`.** It prepended `onboarding = false` and reverted `agent_panel_sort` from `"priority"` to `"spaces"`, leaving the value contradicting its own comment. The rewrite also propagated: `config.toml.example` was copied from the live file afterwards and shipped the reverted value. Herdr owns this file — it is not a one-way input, and a UI toggle or the first-run onboarding can silently undo a setting. `config.toml.example` now carries a note saying so, and omits the machine-specific `onboarding` flag |
| F5  | Medium   | Rendered panes collapse long output (`… +N lines`); scraped answers truncate. Retired by writing answers as files                                                                                                                                                                                                                                                                                                                                                                                                                                 |
| F6  | Medium   | A 180s relay timeout exceeded Claude Code's 120s default Bash tool timeout, so when the planner was the caller the tool call died before the script's graceful fallback printed. Default is now 90s                                                                                                                                                                                                                                                                                                                                               |
| F7  | Medium   | Nothing pinned the pair to a repo — it inherited the planner's cwd, which could start the builder outside the project, away from its conventions file and outside Codex's trusted-projects list. Fixed with `tandem-pair --cwd`                                                                                                                                                                                                                                                                                                                    |
| F8  | Medium   | The relay was one-directional in practice: the builder's conventions file told it about `tandem-ask`, nothing told the planner. Fixed with a planner-side conventions file                                                                                                                                                                                                                                                                                                                                                                         |
| F9  | Low      | Dry-run printed a literal `<new-pane>` in the `report-metadata` line, which reads like a pane name rather than a placeholder. Now says `<id assigned by the split above>` — the real id does not exist until the split runs                                                                                                                                                                                                                                                                                                                       |
| F10 | Low      | `tandem-handoff` used `stat -f`, which is BSD/macOS only; GNU/Linux spells it `stat -c '%Y'`. Now probes with `stat -f '%m' .` once and picks the right flavour. Verified both branches by shimming a GNU-style `stat` onto PATH: both select the same plan                                                                                                                                                                                                                                                                                        |
| F11 | High     | Unquoted heredoc in the launch prompt executed backticks. `--dry-run` ran a shell command while rendering text. See invariants above                                                                                                                                                                                                                                                                                                                                                                                                              |
| F12 | Critical | `idle` does not mean ready — native modals are invisible to Herdr and swallow input while it reports `idle` + `interactive_ready: true`. Defeats any fix keyed on `blocked`, including F2's                                                                                                                                                                                                                                                                                                                                                       |
| F13 | High     | `agent start` races the shell. `pane split` returns as soon as the pane exists, but `agent start` needs it at an interactive prompt: `agent target pane ... is not an available shell`. The identical call succeeds seconds later                                                                                                                                                                                                                                                                                                                 |
| F14 | Critical | Codex's sandbox blocks the Herdr socket. See "Why there is a mailbox"                                                                                                                                                                                                                                                                                                                                                                                                                                                                             |
| F15 | Critical | `tandem-pair` started the builder on Codex's stock interactive defaults — `herdr agent start` accepts `-- <agent-args...>` and nothing was passed. `codex doctor` reported `approval policy OnRequest`, so the builder stopped at a dialog on its first out-of-sandbox action and sat there unwatched. A second, separate gate: the repo was absent from Codex's trusted-projects list, so launch itself opened a native folder-trust modal — invisible to Herdr per F12. Fixed by passing `--ask-for-approval never --sandbox workspace-write` and recording the trust before launch |
| F16 | Medium   | Codex **silently skips** hooks it has not accepted rather than prompting for them (codex-cli 0.151.0). Measured by launching a builder and reading which `SessionStart` hooks reported: the two carrying a `trusted_hash` ran, `superpowers`, which had no `hooks.state` entry despite being enabled, did not, and no dialog appeared. This inverts the risk from F12's era — the hazard is no longer a stall but a builder running with plugin hooks inert while looking healthy. `tandem-pair` therefore warns by default and refuses only under `HERDR_STRICT_HOOK_CHECK=1`. `trusted_hash` is over an internal canonical form: it matches neither the hooks.json bytes, nor the hook command string, nor the script it invokes, so the check detects *unregistered* and *changed-since-last-pairing* instead of recomputing it |
| F17 | Critical | GitHub Copilot CLI 1.0.82 has **two** startup gates, neither of which tandem knew about, and the adapter shipped claiming it had none. (1) A folder-trust modal on first use of a directory; Herdr reports the agent `blocked`, and trust persists to `trustedFolders[]` in `$COPILOT_HOME/config.json` **only** when the human picks "Yes, and remember this folder for future sessions" — plain "Yes" trusts that session and records nothing, so a human who picks it meets the dialog again next pairing. (2) A session-restore picker whenever `$COPILOT_HOME/open-sessions-state.json` is non-empty, which `herdr pane close` guarantees by leaving an `Interrupted` entry on every teardown — so it fires on every pairing after the first. **Herdr reports a pane blocked on that picker as `idle`, not `blocked`**, so `tandem-pair` prints `pair ready`, `tandem-handoff` prints that the plan was delivered, and the plan is swallowed by a modal with no warning at any layer: the F12 class of failure, but silent at every level. Measured by pairing twice against the same trusted path — the second showed the picker listing the first builder's session. Fixed by a Report-only `project-trust` gate (config.json is Copilot's own and is JSONC, so it is never written) and by passing a fresh `--session-id` per builder launch, which suppresses the picker; no flag or setting disables it otherwise. The original "no folder-trust dialog" claim came from an empty `strings` probe of the Copilot binary — a compressed single-executable bundle, so absence of signal was read as evidence of absence |
| F18 | Medium   | Measured 2026-09-25 through Herdr 0.8.2, submitting each CLI's new-conversation command with `herdr agent prompt NAME CMD` (no `--wait`: a reset never goes `working` long enough for `--wait` to count on it). **codex-cli 0.155.1 `/new`** starts an empty conversation and keeps the pane's scrollback, but the `agent_session.value` Herdr reports changes only when the **next** turn starts: Codex creates the thread lazily, and its `SessionStart` hook fires then, not at `/new` — a builder that has never taken a turn reports no session at all. **Claude Code 2.1.282 `/clear`** changes the id immediately; Claude redraws its own screen, so earlier turns leave the view. **Copilot CLI 1.0.87** could not be verified: the account had no allowance left for the month, so no turn could run. After `/new` the pane also stopped accepting typed input while Herdr reported it `idle` (prompts and `/exit` were swallowed) — plausibly a side effect of the exhausted allowance rather than of `/new`, so it is a reason to re-measure, not evidence against Copilot. Re-measure when the allowance resets and set its `ResetCommand` from that. **opencode 1.18.26**: Herdr never reports an `agent_session` for it. So only codex and claude declare a reset command. Because codex's proof arrives a turn late, `tandem agent reset` reports `sent` when the pane is idle again but the id has not moved, and `tandem-handoff` confirms after delivery with `tandem agent session --not <old id>` before recording which session holds the build. **Re-measured 2026-10-11**, Copilot CLI 1.0.96-2 and opencode 1.18.34 through Herdr: **Copilot `/new`** empties the conversation, but Herdr keeps reporting the `--session-id` the pane was launched with and its `state_change_seq` did not move, so Herdr shows nothing; Copilot's process log (`logs/process-<ms>-<pid>.log`) records `Registering foreground session: <id>` at once, and that is what confirms its reset. Copilot writes the new conversation to `session-state` only at its first turn. **opencode `/new`** empties the conversation, but nothing records it until the next prompt: no `agent_session`, no counter move, no session row, log line or state file. So copilot now declares `/new` and opencode still declares nothing |
| F19 | Medium   | Under codex-cli 0.155.1 code mode a shell command has **two** yields: the inner `exec_command` `yield_time_ms`, and the outer `exec` wrapper's, which defaults to 10,000 ms and is set by a first-line `// @exec: {"yield_time_ms": N}` pragma. Measured on 2026-09-25: the builder ran a ~20s check with an inner yield of 1,000 ms and polled it with `write_stdin` 21 times (3.0M input tokens). Raising only one yield still returns early. The launch prompt therefore says "raise every one of them" rather than naming either, staying builder-neutral (tests/test-handoff-waiting.sh) |
| F20 | High     | Measured 2026-09-26 in a real project: Herdr reports a finished turn as `done` until someone looks at the pane, and `idle` after. `ResetDecision` and handoff's delivery check tested for `idle` only, so an unattended re-review left the builder `done`, the reset was skipped, and the build began at 197k tokens (2.6M before Codex compacted); the same test reported delivery the instant a prompt reached a `done` builder. The token-burn eval missed it because it simulates the reset and never exercises Herdr. Fixed by `agent.Settled` (idle or done) in the reset decision, the reset wait and both delivery checks; later review rounds now also start fresh and read only the diff (`tandem gate scope`). Re-measured live 2026-09-27 (codex-cli 0.155.1, the 15-line eval fixture plan), builder `done` before each step: review 1 first call 21.8k, 16 calls, 494k input; diff re-review 22.1k, 7 calls, 173k input, read no plan and ran no check; build 22.6k first call (yesterday 197k), 23 calls, 994k input. The re-review cost 35% of review 1, not the 25% aimed for: on a plan this small nearly all of it is the ~22k per-call entry fee (7 calls ≈ 155k), so the ratio must be judged on a real, 1,000+-line plan. |
| F21 | High     | Measured 2026-09-28 in a real project, codex-cli 0.157.1: `/new` now opens a native "Where should the new conversation run?" picker (Current checkout / New worktree). Herdr cannot see it: the pane stayed `idle` and `state_change_seq` did not move for the 3.1s until `tandem agent reset`'s timer reported `sent`, so "idle for a while" was never evidence of a reset. Handoff typed the launch prompt into the picker, which dropped the text; its Enter picked the default, so the builder went `working` for ~2s to open the new conversation and handoff reported "now working". The Codex session logs showed no turn until a second handoff four minutes later. Recording the unmoved id also wrote the review's session as the build's, which would let a later handoff skip a needed reset. Reproduced in a throwaway pane (probe prompt lost the same way; a second run showed `/new` alone left in the picker for 20s). Fixed: a reset is proven by change, not time -- a moved id, or `state_change_seq` advancing and settling; with neither after 2s the kind's `ResetConfirmKey` (codex: `enter`) is pressed once; still nothing is `unconfirmed`, and handoff and review now send nothing and exit 7 instead of typing into an unmeasured screen; an id that did not move after a reset is no longer recorded. Re-measured live: picker at 0s, Enter at ~2s, new conversation `working` (seq 75) then `idle` (76), `sent` at 6.0s, probe typed at 6.4s reached the new conversation and was answered. |
| F22 | High     | Measured 2026-09-28 in a real project, codex-cli 0.158.0, after F20 and F21 shipped: review, diff re-review and build each started fresh at 28-29k, yet an 8-task build spent 15.5M input tokens in 106 calls and stopped in task 5 with `usage_limit_exceeded` (the 5-hour window at 100%). Its first 21 calls read `AGENTS.md` in full although codex had already injected it (41k characters, held twice), the whole 2,229-line plan, the spec, three superpowers `SKILL.md` files and a tool-description dump: 29k to 93k before task 1. One conversation then carried every task forward (task 5 began at 208k), and two Jest runs added ~40k characters each. A replay of the same timeline with each task fresh at 35k costs 4.4M. Fixed by per-task builds (`internal/plan`, `internal/build`, `tandem build`, `tandem check`, `tandem-handback --task`, the bridge driving `tandem-handoff --continue`), a prompt that never asks for the whole plan and asks for the conventions file only when the CLI does not load it (`Adapter.InstructionFiles`), and `task_input_budget`. Re-measured live the same night on the eval fixture's 3-task plan: each task's first call 22.5k-23k, no plan or `AGENTS.md` read, 12-14 calls and 0.39-0.41M per task (0.46-0.58M with the reviewer subagent each spawned), loop finished unattended, report accepted by `tandem-unpair`. On a plan that small the split costs more than one conversation (1.19M against 0.99M on 2026-09-27): each task pays the ~22k entry fee per call and nothing accumulates to save. The saving is in plans whose tasks carry real reads forward. |
| F23 | Medium   | Measured 2026-09-29 in a real project (codex-cli 0.158.0). A plan put failing tests in a task of their own; tandem checks every task and stopped it red, twice, the second time after a re-handback that changed nothing. The stop message said `tandem-handoff` "sends task 1 again in a fresh conversation", but `build-session` survived the stop, so the re-review and the rebuilt plan both ran in the old conversation (`skip:holds-build`). The final task then stopped `stuck` although tandem's own check exited 0: the builder's sandbox had killed its full Jest run when a test reached a private address, and "stuck beats everything" held, correctly, since a green check does not prove the task was finished. Fixed: a stop or finish removes `build-session`; `tandem build accept --task N --because` lets the planner overrule a red or stuck stop on the record; the stop message stops suggesting an unchanged re-handback; the plan review counts a task that must end red as an impossible step; `tandem-relaylog` skips the triage verdicts it printed as `Q: null`. |
| F24 | Medium   | Measured 2026-09-29 in a real project, codex-cli 0.158.0. With `HERDR_JUDGE=1`, `tandem agent launch-spec` gives the Codex builder a network scope (only `api.typesafe.ai`, `--enable network_proxy`). In a real `codex exec` agent with those settings, a request to a blocked host makes Codex report the command as failed (`exec_command failed: ProcessFailed ... Network access to "stream1.m3u8" was blocked`) even though Node caught the error and the command exited 0. With `-s workspace-write` and no network the same request fails quietly (`ENOTFOUND`) and the command succeeds. `codex sandbox` alone does not reproduce it; only a real agent's exec tool does. Every project paired with Jev triage on is affected: a test that touches the network is reported to the builder as a failed command, which can make it stop or rework a task that passed. Not fixed yet. It strengthens Phase 3 Task 8 of the audit fix plan: once tandem calls Jev from its own side after the handback, the builder needs no network scope and the two behaviours above stop differing. |
| F25 | Medium   | Tandem now calls Jev after the task handback, with its own check result and the task's diff. The builder needs no Jev network scope or key, so the blocked-host command failure described in F24 cannot occur through triage. The judgment sees the true check result; on one real build the builder's own result was always "failed". Deleted `tandem-judge`, `tandem decision judge` and the launch-scope probe. |
| F26 | High     | Measured 2026-10-12, codex-cli 0.162.1 under Herdr. Codex runs every conversation on one shared app-server daemon (`codex app-server --listen unix:// --manage`), which the first Codex TUI on the machine starts and every later TUI joins; `--disable daemon_auto_start` only stops a TUI starting one. Herdr's Codex `SessionStart` hook runs inside that daemon and reads `HERDR_PANE_ID` from the daemon's environment, so every Codex conversation is reported against the pane that started the daemon: that pane's `agent_session` keeps changing to other panes' conversations, and no other Codex pane ever gets one. A daemon started outside Herdr reports none. tandem had read a missing session as "no turn yet" (`skip:no-session`), so a Codex reviewer was never reset before a plan review, and a moved id as a proven reset. Now Herdr's session is read as the pane's conversation only for Claude (`herdr.Agent.Conversation`), a missing one means "no turn yet" only for Claude and Copilot (`HasNoConversation`), a Codex reset is proven only by the pane's own `state_change_seq`, and a Codex task's conversation is the one whose log holds its nonce, for usage and for judging a turn that ended at once. `/new` then Enter at the picker moved the counter the same way on a pane with a conversation and on a new one. |
| F27 | High     | Herdr 0.9.3's event stream, checked live 2026-10-12. Status changes arrive as `pane.agent_status_changed`, and every other kind is spelled with underscores (`pane_updated`, `pane_agent_detected`, `pane_exited`, `pane_closed`, `pane_moved`), so a reader normalizes `.` to `_`. A quitting agent sends `pane_agent_detected` with `released: true`, never `pane_exited`. A moved pane gets a new id, and the event carries `previous_pane_id`. `pane_updated` cannot be filtered by pane on the server and arrives often. A `pane.agent_status_changed` subscription requires `pane_id` in 0.9.3: without it Herdr answers `invalid_request` (`missing field pane_id`), and for a pane it does not have it answers `pane_not_found`. So a watch reads each agent's pane before it subscribes, and opens once more if the pane moved or went away in between. A reset therefore proves itself by a status change only, never by a session-only `pane_updated`. The socket is `HERDR_SOCKET_PATH`, else the `socket:` line of `herdr status server`. |
| F28 | Medium   | fsnotify v1.10.1 on macOS 26 (kqueue), probed 2026-10-13 for the mailbox watcher. A writer's `mv x.json.tmp x.json` arrives as `CREATE x.json` and `RENAME x.json.tmp`; the bridge's claim `mv -n x.json x.json.taken` as `CREATE x.json.taken` and `RENAME x.json`. kqueue learns new names by rescanning the folder, so a burst merges: a file written, renamed and claimed in quick succession never showed `CREATE b.json`. A watch is a hint and the bridge still lists its queues on every wake. Replacing `active-run.json` by rename gives `REMOVE` and `CREATE` naming it. `Add` on a missing folder fails with `lstat`. kqueue holds one handle per file in a watched folder (200 files: 11 to 215 open handles); macOS's soft limit is 256, but the Go runtime raises it to the hard limit at start. `InitRunDir` creates `durable/<run>/q` before it writes the marker, so the marker event is enough to see a new run. |

### Predictions that were wrong

Recorded because they were plausible and cost time:

| Suspected                                             | Verdict                                               |
| ----------------------------------------------------- | ----------------------------------------------------- |
| `[[keys.command]]` popups lack `~/.local/bin` on PATH | **False** — the Herdr server's PATH includes it       |
| `(( WAIT )) && cmd` aborts under `set -e`             | **False** — the script survives                       |
| Claude Code's TUI is unreadable (alternate screen)    | **False** — `agent read` returns a legible transcript |
| Rapid sequential CLI calls are flaky                  | **False** — 0 failures in 10                          |

A related trap: `codex sandbox` defaults to **read-only**, not the `workspace-write` the
agent actually uses. Testing with the default reports failures that do not occur in
practice.

---

## Herdr configuration

`config/config.toml.example`. Only purposeful changes; no theme is imposed.

| Setting                             | Why                                                                                                                      |
| ----------------------------------- | ------------------------------------------------------------------------------------------------------------------------ |
| `status_indicators = "symbols"`     | The default `"dots"` renders blocked, working and done as identically-shaped marks — you cannot tell why a pane is quiet |
| `agent_panel_sort = "priority"`     | A blocked agent floats to the top of the attention queue                                                                 |
| `[ui.toast] delivery = "system"`    | Default is `off`; without it nothing tells you a background agent needs input                                            |
| `show_agent_labels_on_pane_borders` | Names the detected agent per pane                                                                                        |
| `resume_agents_on_restore`          | Reattaches **both** native conversations after a server restart mid-plan                                                 |
| `rows_by_agent` with `$role`        | Role-first sidebar rows, colour-coded per agent                                                                          |

**Custom sidebar tokens must start with `$`.** Established by probing `herdr config check`
with `HERDR_CONFIG_PATH` against throwaway files: `{ token = "role" }` is rejected,
`{ token = "$role" }` accepted, and a bad hex colour is rejected. The validator is strict
about keys, enums, token names and colours, so `config: ok` is meaningful.

Keybindings avoid `alt+` chords deliberately — Herdr's own defaults warn they depend on the
terminal or tmux. `prefix+i`, `prefix+shift+h` and `prefix+a` do not collide with any
default binding.

---

## Exit codes

`tandem-ask`:

| Code | Meaning                                                           |
| ---- | ----------------------------------------------------------------- |
| 0    | reply printed                                                     |
| 1    | usage error, guard failure, or unresolvable name                  |
| 3    | peer blocked at a dialog — **nothing was sent**                   |
| 4    | prompt delivered but no lifecycle change (`agent_prompt_stalled`) |
| 5    | no answer within the timeout — bounded fallback                   |
| 6    | peer settled but returned no readable text                        |

`tandem-handoff` adds **7**: prompt accepted but the builder never left `idle` — delivery
almost certainly swallowed by a native modal.

`tandem-unpair` adds **4** (never handed back), **5** (handback not signed off), **6** (no
`check_command` to verify with), **7** (the check failed) and **8** (the check no longer
describes the work: a handback, the tree or the builder moved while it ran).

`tandem-handback` reuses 1, 3 and 5 with the same meanings, adds **2** (no agent named
`planner`) and shares **7** with `tandem-handoff`: prompt accepted, peer never woke. Every
one of its failure paths prints the report file it already wrote, on its own line, because
the failure a builder must not respond to is "the handback bounced, so redo the work".

The 7 check only runs when the planner was `idle` before the prompt. A planner that was
already `working` queues the prompt without changing state, so there is nothing to observe
— and a check that cannot distinguish success from failure must not report either. The
status is re-read after waiting out a `blocked` planner, or the check would skip itself on
exactly the agent it was written to test.

Failed relay delivery writes an error answer into `a/<id>.md`, so a sandboxed caller stops
polling with a reason instead of timing out blind. A handback has no such caller — its
sender has already exited — so a failed one drops `reports/<id>.UNDELIVERED` beside the
report instead, where whoever goes looking for the missing work will find it.

**Every code above survives `tandem <verb>`.** The dispatcher hands over with `execve`
rather than running a child, so the script replaces the process and its status reaches the
caller untranslated. This is not incidental: `tandem-unpair` gates a teardown on the exact
numbers in these tables, and a wrapper that observed a child and re-exited would be one
layer where 23 could arrive as 1. `tests/test-dispatch.sh` measures it.

**Silence is ambiguous, so the wait is not silent.** Mailbox mode prints a progress line to
stderr every 10s. Observed without it: the builder's command runner returned without an exit
code, the builder tried to `ps` for the call, its sandbox denied process inspection, and it
resolved the ambiguity by asking the same question again — two deliveries and two
interruptions for one intent. The heartbeat carries the "do not re-run" advisory in every
beat, which is where a confused caller is actually looking. Progress goes to stderr so
stdout stays exactly the reply.

---

## Verifying a change

- `herdr config check` validates `config.toml` and is strict enough to be meaningful.
- `--dry-run` is on every command that touches another agent: `tandem-pair`,
  `tandem-handoff`, `tandem-ask`, `tandem-handback`, `tandem-unpair`. `tandem-bridge` has
  `--once` instead, `tandem-project` only prints, and `tandem-init` has no preview at all — it writes `.tandem.json` and appends to
  `.gitignore`, so read what it wrote.
- `herdr api schema` returns the socket API's JSON shapes — but see F1: the CLI does not
  always return the envelope the schema describes. Confirm against the actual command.
  The event stream does match the schema's shapes, apart from the two spellings of event
  names in F27.
- The six commands that talk to the server (`tandem-pair`, `tandem-handoff`, `tandem-ask`,
  `tandem-handback`, `tandem-unpair`, `tandem-bridge`) require `HERDR_ENV=1` and refuse to touch a session from outside Herdr.
  `tandem-project` and `tandem-init` deliberately do not: inspecting or initialising a repo
  is pure filesystem work and should not require a running Herdr session. Both do now
  require the compiled `tandem` on `PATH`, because they ask it which manifest and mailbox
  names this repo uses rather than assuming.

End-to-end, from a planner pane in a target repo:

1. `tandem-init` then `tandem-pair`, and confirm `herdr agent list` shows both names
2. Sidebar shows the role rows and symbol status indicators
3. `tandem-handoff --dry-run` selects the expected plan and renders the prompt instantly —
   if it is slow, something in the prompt is executing
4. Background the tab, drive the builder to `blocked`, confirm a notification fires —
   only applies if you took `[ui.toast] delivery = "system"`; the default is `off`
5. From the builder pane: `tandem-ask planner "test"` returns an answer
6. From the builder pane: `echo test | tandem-handback` reaches the planner, and the report
   file named in the message exists and holds what was piped in
7. `tandem-unpair --dry-run` names the right pane, pid and mailbox; then `tandem-unpair`
   closes the pane and the bridge process is gone
8. Restart the server and confirm both conversations resume

The bridge can be exercised without a live pair: put a `herdr` stub earlier on `PATH` that
records `agent prompt` instead of sending it, hand-write a `q/<id>.json`, and run
`tandem-bridge --once`. That covers both kinds and both delivery outcomes without needing a
second agent.

---

## Where the Go core sits, and why the prose did not move

Part of this harness is compiled. `tandem` holds the herdr JSON decoding, the Codex
hook-trust classification, the four teardown gate verdicts, and the resolution of where
tandem's own files live (`internal/paths`); the twelve commands in `bin/` stay bash. It is
also their front door — `tandem pair` hands over to `tandem-pair` — but that is routing,
not deciding, and it is a PATH lookup precisely so the binary never has to know what the
scripts are. The rule that draws the line is:

> **Go decides. Bash explains.**

The gates are mostly prose. Gate 2 is a two-line boolean — `reports_ok + reports_pending
== 0` — wrapped in twenty lines explaining to a human why an idle builder is not a
finished one. Moving that wholesale into Go would turn every refusal into a string
literal, which is worse to edit and worse to read, and this document's whole premise is
that the reasoning is the valuable part. So Go returns a verdict plus a machine-readable
detail token, and bash prints the heredoc it already had. Every word a human sees is
still in the script that shows it.

Three things follow from that line, and each was learned the hard way:

**The facade uses the CLI for actions and the socket for events.** Prompt, read, start,
send-keys, get and list go through the `herdr` CLI, because the sandbox detection in
`Raw` is built on how the CLI fails. Every wait for an agent's status, session or
departure reads `events.subscribe` on the socket (`internal/herdr/events.go`), because a
poll misses whatever happens between two reads. `tandem pane wait-shell` is the one
Herdr wait that still checks on an interval, reading `pane process-info` every 250 ms,
because Herdr sends no event when a pane's foreground process becomes its shell. Herdr 0.9.3 ships a versioned schema (`herdr api schema`:
protocol 22, schema_version 1), which is what made the socket safe to depend on. F27
records the shapes read.

**`internal/gate` may not make a herdr call.** Gate 4 re-reads the builder's status to
catch it waking mid-check — so bash does that read and passes it in as `--status-now`.
The constraint is enforced transitively with `go list -deps`, and it earned its keep
before any code existed by forcing that flag into the design.

**Unreachable is not the same as refused.** Under Codex's seatbelt the herdr CLI dies
before it can format a response and prints a raw `Error: Os { ... PermissionDenied ... }`
instead of the JSON envelope. `tandem` therefore treats *any* unparseable answer as
unreachable (exit 3), which is what routes a sandboxed agent to the mailbox relay. An
earlier version matched the string `PermissionDenied` inside the decoded JSON, which that
error never reaches — and handback delivery from the builder silently failed until the
builder's own handback failed to arrive and exposed it. The bash it replaced was broader
on purpose: `if ! herdr agent list >/dev/null 2>&1; then ask_via_mailbox; fi`.

One gap is known and deliberate. `tandem-bridge` keeps its own `jq` and its
`// "unknown"` defaults, because it must keep working when everything else is broken.
`tandem-pair` used to parse herdr JSON with `jq` too, for topology decisions the by-name
snapshot does not model. Those moved into `tandem agent planner`, `tandem agent adoptable`,
`tandem pane split-direction` and `tandem tab label`, each a function in `internal/herdr`
with its own tests. Its `codex_hook_trust` logic had already moved, and is twenty-one
table-driven subtests.

One thing the Go version does that the bash could not: `tree_fingerprint` swallowed git
errors with `|| true`, so an empty result meant both "clean tree" and "git unreadable",
and the comparison was skipped for both. A clean tree going dirty during the check was
therefore invisible — the exact change gate 4 exists to catch. `vcs.Fingerprint` returns
an error, so the two are now distinguishable and only the unreadable case is skipped.

---

## Why the review was builder-side, and why it moved

Superseded on 2026-10-18 by the reviewer block.
The builder no longer reviews. A separate reviewer pane, declared in `.tandem.json` or
started with `tandem-pair --reviewer KIND`, takes every plan review and every task review,
and the gate refuses a review the builder wrote (`builder_review`). The owner's rule is that
the agent that builds never judges its own work. The reasoning below is the original case,
kept for the record. Its point about authority still holds, which is why a review stays
bound to the builder's kind and autonomy as well as to the reviewer's kind and model.

The planner wrote the plan, so it cannot audit it — the same conflict that
makes an unenforced "please check first" worthless at sign-off. The builder is
the only party in the loop with an independent read, and it was already doing
the work: across four measured sessions it raised eleven objections, every one
a static property of the plan or of its own authority, and every one raised
mid-build after the cost had been paid.

A separate reviewer agent was considered and deferred. Roughly five of the
eleven are plan-text findings a smaller model could plausibly make; the rest
needed the source, a test run, or the builder's own sandbox. And the most
expensive class does not transfer at all: "I cannot commit here" is a property
of *this* builder's authority, so a reviewer with different autonomy would
report the plan feasible and be wrong.

## Why the verdict is asserted, not parsed

`tandem-verdict` takes `--verdict clean|objections` rather than reading the
review prose for objections. A parser that failed to find them in a malformed
review would report clean, and the gate would open. The reviewer asserts its own
verdict for the same reason the planner names `--verified <id>`: an explicit act
about a specific piece of work is worth more than an inference, precisely
because it can be wrong and be seen to be wrong.

## Why nothing in a review is hashed

For the reason the tree fingerprint is not hashed, recorded above: a hash puts
the gate behind `shasum` and a missing hasher fails open. A review stores a
verbatim copy of the plan it read, compared as text — which also lets the
refusal show a diff of what changed rather than only asserting that something
did.

## Why the builder identity is recorded once, not resolved twice

`internal/config` reads `HERDR_BUILDER_KIND`, `HERDR_BUILDER_AUTONOMY` and the
legacy sandbox/approval variables from the process environment, and honours a
per-user config path — while `tandem-pair` launches the builder's pane with
only `HERDR_ROLE`. If both `tandem-verdict`, running in the builder's own
pane, and `tandem gate review`, running in the planner's, each called
`tandem config resolve` for themselves, the two calls would run in two
different environments and could disagree with nothing having changed. A
disagreement would refuse `builder` on *every* attempt, and without self-healing:
each review the planner requests to clear the refusal re-captures the same
mismatched value and fails the same way.

So `tandem-pair` resolves the builder's identity once, when it starts or
adopts the pane, and writes it into `builder.json` in the mailbox. Both
`tandem-verdict` and `tandem gate review` read that record; neither resolves
it again, and `tandem gate review` takes no `--kind`/`--autonomy` flags at
all — there is one reader, and no flags for a caller to get wrong. A missing
or incomplete record is *not* treated as a match: the gate refuses `builder`
rather than comparing two empty strings.

The known cost is one already documented for `tandem-pair` itself: for an
*adopted* builder the recorded identity is what tandem asked for, not
necessarily what the running agent was actually started with. Recording it
makes that gap visible instead of hiding it inside a comparison that always
happens to pass.

### Where that record lives: two anchors, one rule each

A pair writes into two directories, and which one depends on *what* is being
written:

| what | anchored on | who |
|---|---|---|
| `builder.json`, `reviewer.json`, `reviews/` | the builder pane's own cwd, **unwalked** | written by `tandem-pair`, read by `tandem-handoff`, `tandem-review` and `tandem-verdict` |
| `q/`, `a/`, `bridge.pid`, the gitignore entry | the **git toplevel** (walked) | the relay bridge, and `tandem-ask` / `tandem-handback` |

The identity path must not walk, because its readers do not: `tandem-project`
calls `rev-parse --show-toplevel` only when its `--root` is empty, and both
`tandem-handoff` and `tandem-verdict` always pass a non-empty one, which it
echoes back verbatim. Walking at the writer would make a builder pane whose
cwd is a subdirectory disagree with both readers — and it is not hypothetical:
that walk was removed from `tandem-verdict` for this exact reason, then
reintroduced at the writer and removed again.

The relay path walks for its own older reason. The two coincide at the repo
root and in a non-git directory, and diverge everywhere else — which is a
trap for anything that has to clean up after a pair. `tandem-unpair` resolves
**both**: removing only the walked one leaves the builder identity and the
whole review history behind, and a stale review whose plan text and builder
identity still match would pass the gate in a later session, handing over a
plan on a review nobody made for it.

Resolving both needs the builder's cwd, and `agent snapshot` — the live
source for it — cannot answer once the builder pane is gone, which is exactly
when teardown tends to run. `tandem-pair` therefore records that cwd in the
**walked** mailbox, the one that always resolves from anywhere in the repo,
and `tandem-unpair` falls back to it when the pane is unreachable. It stores
the directory rather than the mailbox path, so teardown re-derives the mailbox
and its `rm -rf` guard still compares against a fresh resolution rather than
against a string it was handed. A session paired before that record existed
cannot be resolved at all; teardown says so rather than passing over it.

## The plan wins, except for one thing

The launch prompt's contract is unambiguous: "If the plan contradicts anything
in this message, the plan wins." An overruled review would be inert against
that rule — a ruling of "skip the commit steps" directly contradicts a plan
that says commit, and plan-wins would send the builder straight back into the
objection it already raised.

The `OVERRULED OBJECTIONS` block that `tandem-handoff --override-review`
injects is written as the one explicit exception: "These rulings outrank the
plan — they are the only part of this message that does." It carries the
builder's own objections alongside the planner's ruling, not the ruling alone,
because a bare ruling read cold does not say which steps it covers or why —
and the builder reading it is not always the one that raised it in the first
place.

**Every substituted value is defanged, so the order of the passes cannot
matter.** `${var//pattern/replacement}` is a global replace over the whole
accumulated string, so each pass rescans everything written by the passes
before it. Any value carrying literal slot syntax — `prompt_extra` is
arbitrary per-repo text, but so are a plan filename, a check command and a
conventions-derived prelude — could therefore be filled in by a *later* pass
and corrupt what an earlier one had already written. The worst case is a repo
forging `{{OVERRULED}}` and receiving a second copy of the planner's ruling in
a place the planner never put it.

`defang_slots` closes that by rewriting `{{SLOT}}` to `{SLOT}` in every value,
for every one of the seven slot names, before it enters the template. A
defanged value has no live marker left for any pass to fill, so no ordering
between the passes is load-bearing.

It rewrites **to a fixed point**, not in a single pass. One pass is not enough:
`{{{OVERRULED}}}` reduces to a live `{{OVERRULED}}`, which the next
substitution would then fill — a single-pass sanitizer is always defeated when
its own output can re-form the pattern it removes. Each changing pass strictly
shortens the value, so the loop terminates structurally; the pass cap exists
only to bound adversarial nesting, and hitting it **refuses** rather than
passing a partly-defanged value through, which would reopen the hole the loop
closes.

`{{OVERRULED}}` is still substituted last. That is no longer what makes it
safe, but it costs nothing and keeps the ruling the final word in the
rendered prompt.

**A ruling stands until the plan changes.** The `--because` reason is
persisted into the review's sidecar rather than re-read from the command line,
so the planner does not retype it on every re-handoff of a plan nobody has
touched. That makes the *second* handoff the one to get right: it carries no
`--override-review`, and the gate clears it on the stored reason alone. The
gate therefore names the review it judged on the pass path too, and
`tandem-handoff` renders the `OVERRULED` block from that record — otherwise a
re-handoff would deliver the plan with the ruling and the objections both
missing, which is precisely the failure this whole section exists to prevent.

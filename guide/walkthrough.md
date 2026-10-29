# A worked example

Shipping a feature called "add rate limiting".

## 1. One-time setup in the target repo

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

[How the builder is launched unattended](launching.md) covers the trust dialogs and
approval flags `tandem-pair` handles for each kind, and when it adopts a running agent.

## 2. The planner does the thinking

You brainstorm and write an implementation plan into whatever `plans_dir` resolved to, e.g.
`docs/plans/2026-08-29-rate-limiting.md`.

**This part is deliberately out of scope.** The harness automates the handoff and the Q&A
transport, not the thinking, which is why `tandem-handoff` has no opinion on plan format —
it just picks a file.

## 3. The reviewer attacks the plan

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
any pane but the reviewer, by the rule in
[Which pane is the reviewer](reviews.md#which-pane-is-the-reviewer). That page also covers
when a review is reused, what voids it, and how to overrule one.

## 4. Hand the plan over

```bash
tandem-handoff --dry-run    # renders the launch prompt without sending it
tandem-handoff              # sends it; defaults to the most recently reviewed plan
tandem-handoff docs/plans/2026-08-29-rate-limiting.md    # or name it explicitly
```

The builder gets **one task at a time**, each in a fresh conversation, and tandem sends
the next task itself. See [Per-task builds](builds.md#per-task-builds).

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

[Builds](builds.md) covers the fresh conversation each task gets, what stops a build and
how to carry on. [Optional Jev features](jev.md) covers staffing advice and review triage.

## 5. The builder implements, and asks back

Mid-implementation Codex runs:

```bash
tandem-ask planner "should rate limiting be per-IP or per-API-key?"
```

Codex is sandboxed and cannot reach the Herdr socket, so this writes a question into
`.tandem-relay/q/`, `tandem-bridge` delivers it to the planner pane as a prompt, and the
planner's answer is written to `.tandem-relay/a/<id>.md` for Codex to poll. The answer is
**written as a file, never scraped from a terminal**, so nothing is lost to the terminal
collapsing long output.

## 6. Ask in the other direction too

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

## 7. The builder hands back, and the planner closes the session

For a build, the builder hands back each task with `tandem-handback --task N`, and tandem
writes the one report the planner receives when the build ends (see
[Per-task builds](builds.md#per-task-builds)). The rest of this section is about that report, and
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
`check_paths` in `.tandem.json` (see [Per-project configuration](configuration.md#per-project-configuration)), so an
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

## 8. Sanity-check the loop on a new repo

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

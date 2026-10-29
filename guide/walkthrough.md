# A worked example

This page follows one feature, "add rate limiting", from setup to closing the pair.

## 1. One-time setup in the target repo

```bash
cd ~/projects/my-app
tandem-init            # writes .tandem.json from detected or declared settings
tandem-pair            # names this Claude pane "planner", starts or adopts a Codex
                       # pane named "builder" and the declared "reviewer", and
                       # starts the relay bridge
herdr agent list       # check that all three names appear
```

**Point the builder at the repo if your planner pane isn't already in it.** `tandem-pair`
takes the builder's working folder from the planner agent's own working folder. That is
where Claude Code is running, which may not be where you just ran `cd`. So:

```bash
tandem-pair --cwd ~/projects/my-app
```

If you get this wrong, the builder starts outside the repo. Its conventions file is
missing there and `plans_dir` doesn't resolve, and both show up later as confusing failures
instead of an error now. tandem checks that `--cwd` is a folder, and
`tandem-pair --dry-run` shows the path it will use. It is also the folder `tandem-pair`
marks as trusted for Codex, so point it at the repo you mean.

[Launching the builder unattended](launching.md) covers the trust dialogs and approval
flags `tandem-pair` handles for each kind, and when it adopts an agent that is already
running.

## 2. The planner writes the plan

You and the planner work out the design and write an implementation plan into the plans
folder, for example `docs/plans/2026-08-29-rate-limiting.md`.

**tandem leaves this part to you, on purpose.** It automates the handoff and the
questions between agents, not the thinking. That is why `tandem-handoff` has no opinion on
the plan's format. It only picks a file.

## 3. The reviewer checks the plan

```bash
tandem-review                          # the most recently reviewed plan, or else the newest, as tandem-handoff picks
tandem-review docs/plans/specific.md   # or name one
```

The reviewer pane reads the plan and the code it touches, and tests what it can actually
do in this repo. Then it returns a verdict with
`tandem-verdict --plan ... --role NAME --review ... --verdict clean|objections`. It looks
for five problems and nothing else:

- a task that needs something a later task adds;
- two tasks that contradict each other;
- a step that can't run as written;
- a step that needs authority the builder doesn't have;
- a test or example in the plan that would not produce the result the plan claims.

tandem doesn't ask the reviewer for design opinions, and the reviewer doesn't give them.
That is why a review is a handful of findings, not a list of preferences.

**The builder never reviews.** With no reviewer recorded in `reviewer.json`,
`tandem-review` sends nothing and names both fixes: declare a `reviewer` block in
`.tandem.json` and run `tandem-pair` again, or run `tandem-pair --reviewer KIND`. There is
no `--builder` flag, and tandem refuses one. `tandem-verdict` refuses a verdict from any
pane but the reviewer, by the rule in
[Which pane is the reviewer](reviews.md#which-pane-is-the-reviewer). That page also covers
when tandem reuses a review, what voids one, and how to overrule one.

## 4. Hand the plan over

```bash
tandem-handoff --dry-run    # shows the launch prompt without sending it
tandem-handoff              # sends it, for the most recently reviewed plan
tandem-handoff docs/plans/2026-08-29-rate-limiting.md    # or name the plan
```

The builder gets one task at a time, each in a fresh conversation, and tandem sends the
next task itself. See [Per-task builds](builds.md#per-task-builds).

**Name the plan when it matters.** With no review on record, tandem falls back to the
newest plan, meaning the file with the newest modification time. That isn't the time
someone wrote it, because a fresh clone or checkout stamps every file at once. In one real
repo, 20 of 55 plans had the same modification time, so tandem picked between 20 files by
filename.

**tandem already checks delivery.** After typing, `tandem agent prompt --as` waits up to 8
seconds on Herdr's event stream for the builder to start working. If it never does,
`tandem-handoff` fails with exit 7. That is how tandem catches a prompt swallowed by a
native dialog Herdr can't see. You don't need `--wait` for that check. Use `--wait` when
you want to block until the builder settles, and see the state it settled in.

**A turn that ends at once is not a delivery either.** Once the pane starts working,
`tandem agent prompt --as` watches it for 8 more seconds. A turn still running then counts
as delivered. For a turn that settles sooner, tandem reads the conversation's own log:

- If the token total grew, the model ran and the turn was just short. A Codex reviewer once
  finished a correct review in 5 seconds.
- If it didn't grow, no model call happened. A model the CLI rejects, an expired login or a
  used-up quota looks like this. `tandem-review` and `tandem-handoff` then exit 8 and print
  the pane's last 20 lines.

If tandem can't read the log, it counts the turn as delivered and prints a note. Wrongly
blocking real work costs more than a late warning, and a durable run that really did fail
pauses later, and the bridge says so. The cost of this check is that a successful send
returns up to 8 seconds later.

[Builds](builds.md) covers the fresh conversation each task gets, what stops a build, and
how to carry on. [Optional Jev features](jev.md) covers staffing advice and review triage.

## 5. The builder asks a question

In the middle of a task, Codex runs:

```bash
tandem-ask planner "should rate limiting be per-IP or per-API-key?"
```

Codex runs in a sandbox and can't reach the Herdr socket. So `tandem-ask` writes the
question into `.tandem-relay/q/`, and `tandem-bridge` delivers it to the planner pane as a
prompt. The planner writes its answer to `.tandem-relay/a/<id>.md`, and Codex polls for
that file. tandem never scrapes the answer from a terminal, so nothing is lost when the
terminal collapses long output.

## 6. The planner asks too

```bash
tandem-ask builder "how far through the plan are you?"
```

It is the same command, but from the planner it goes straight over the socket.
`tandem-ask` tests the socket and picks the mode itself, so you use it the same way from
either side.

**It blocks, and says so.** In mailbox mode it prints a progress line to stderr every 10
seconds (`HERDR_ASK_HEARTBEAT_SECONDS`). A caller that sees nothing for minutes can't tell
"still waiting" from "died". A caller that can't inspect its own processes, as Codex can't
in its sandbox, would ask again. Replies stay on stdout, and progress lines never mix into
them. tandem really delivers a repeated question, so the planner sees it twice.

**The progress line also says whether the bridge is running.** Each progress line checks
`tandem-bridge` itself. The check reads the pid file and the process name, like the checks
in `tandem-unpair` and `tandem-status`. If it can't read the process table, as happens in
Codex's sandbox, it falls back to the pid file alone. So a dead bridge and a busy agent no
longer look the same. tandem names a dead bridge in each progress line and in the final
timeout message, with the fix: run `tandem-pair` again, which is safe on a session that is
already paired.

**tandem records every question that gets an answer**, in either direction and over either
transport, in `.tandem-log/<id>.json` with the fields `id`, `ts`, `from`, `to`, `question`
and `answer`. Read it back with `tandem-relaylog`. Without it, a builder in direct mode
would leave no trace of its questions. The record stays after `tandem-unpair`, which never
touches this folder. A timeout or a failed delivery leaves no record.

**The default timeout depends on the direction.** Asking the builder waits 90 seconds. The
caller is usually Claude Code, whose Bash tool kills a command at 120 seconds. A longer wait
would be killed partway, along with the fallback that tells the caller to carry on and
state its assumption. Asking the planner waits 300 seconds. The caller is the sandboxed
builder, which has no such limit, and the planner may need to read code before it can
answer. A single 90-second default once made a builder give up partway through and act on
its own guess, while the planner was still writing the answer. `--timeout` overrides both,
and `HERDR_ASK_TIMEOUT_SECONDS` changes the default.

## 7. The builder hands back, and the planner closes the pair

In a build, the builder hands back each task with `tandem-handback --task N`, and tandem
writes the one report the planner gets when the build ends (see
[Per-task builds](builds.md#per-task-builds)). The rest of this section is about that
report, and about a plain handback, which a builder can still send at any time:

```bash
tandem-handback --plan docs/plans/2026-08-29-rate-limiting.md --report report.md
```

**The report is a file, and tandem sends the planner its path.** A report is long, and the
terminal collapses long output, which is also why answers are files. The report lands in
`.tandem-relay/reports/<id>.md`.

**The handback doesn't wait for a verdict.** Checking work is slow: reading a diff, running
the tests. A builder that waited would hit its timeout partway through, and report a
failure for a handback that actually arrived. So the handback only delivers. The builder
goes idle and stays available.

**It does check delivery.** `agent prompt` returning 0 doesn't prove delivery. Herdr can't
see native dialogs, and an agent at one reports `idle` while the dialog swallows the input.
So when the planner was idle before, `tandem-handback` confirms the planner actually woke
up. If it didn't, the handback exits 7 with the report path. `tandem-handoff` does the same
check in the other direction.

The planner wakes with the report path and has two choices. If the work is wrong,
`tandem-ask builder "..."` sends it back while the builder still has the context. If it is
right:

```bash
tandem-unpair --verified 20260829-141233-8821-19044    # the handback message gives the id
tandem-unpair --dry-run --verified <id>                # or see what that would do first
```

**Nothing closes the pane by itself**, neither the builder nor the bridge. Closing a pane
deletes its scrollback, so it is always a deliberate step.

**tandem enforces "verified".** `idle` can mean three things: never started, handed back
but not read yet, or handed back and checked. Only the last one may be closed. So
`tandem-unpair` refuses unless every gate passes:

| Gate | Refuses when | Exit |
| ---- | ------------ | ---- |
| not mid-write   | the builder is `working` or `blocked`      | 9 |
| work exists     | the builder has never handed back          | 4 |
| signed off      | a handback has no `--verified`             | 5 |
| check available | the repo has no `check_command`            | 6 |
| check passes    | `check_command` exits non-zero             | 7 |
| check still applies | a handback arrived, the tree changed, or the builder woke up while the check ran | 8 |

In gate 8, "the tree changed" means the whole working tree by default. A repo can limit it
to the paths its `check_command` actually tests, with `check_paths` in `.tandem.json` (see
[Per-project configuration](configuration.md#per-project-configuration)). Then an unrelated
change elsewhere in a large repo no longer causes a false "stale" refusal.

The planner's word covers the first three gates. The check is the only real evidence, which
is why `tandem-unpair` runs it instead of trusting a claim. It runs before anything closes,
so a failing check leaves the pair as it was.

The last gate stops tandem from reusing a passing check that is out of date. A real test
run takes minutes, and the session is live the whole time. The builder can hand back again,
start working again, or change the tree while the check runs. After any of those, the
passing result describes something other than what tandem is about to close. So
`tandem-unpair` records the handbacks and the tree before the check, and compares them
after. It ignores files in `.gitignore`, so a test run's own build output doesn't trip the
gate.

`--force` overrides every gate, and `--skip-checks` overrides only the last two.
`--keep-relay` keeps the reports and their sign-off markers.

**A durable run is signed off on its own evidence.** It writes no handback report, so
gates 4 and 5 read the run from the state home instead of `reports/`. Gate 4 refuses when
no task was accepted. Gate 5 refuses, naming each task and what it is missing, unless every
task was accepted with a passed check and a recorded review for its final tree. A waiver
from `tandem build accept --waive` can stand in for either. You don't need `--verified`.
tandem lists any reports an earlier legacy build left in the relay as not part of the run,
and doesn't gate on them. The other gates apply as before: not mid-write, the check, and
nothing changed while it ran.

`--clean-trust` also removes the project trust entry tandem added for the repository. It
removes only entries with tandem's marker. It recognises and leaves alone an entry you
wrote yourself, or one written before tandem used markers. It skips the removal while
another live agent of the same kind is still working in the repository, because removing
the entry would leave that agent stuck at a trust dialog.

A refusal for an unsigned handback prints the command to close the pair, and the command to
send the work back beside it. The refusal exists to make sending work back as easy as
closing the pair.

## 8. Check the loop on a new repo

```bash
herdr config check                 # checks config.toml
tandem-handoff --dry-run           # finding the plan and showing the prompt should be instant
tandem-ask planner "test"          # run from the builder pane, to check the relay both ways
```

If the second command is slow, something in the prompt is running instead of just being
displayed. See `guide/design.md`.

**`--dry-run` works on every command that touches another agent:** `tandem-pair`,
`tandem-handoff`, `tandem-ask`, `tandem-handback` and `tandem-unpair`. `tandem-project`
doesn't need it, because it only prints JSON, and `tandem-bridge` has `--once` instead.
`tandem-init` has no dry run, and it does write: it creates `.tandem.json` and adds lines to
`.gitignore`. It won't overwrite an existing manifest without `--force`, but read what it
wrote afterwards.

# Plans and reviews

## Plans

Each plan is one Markdown file in the plans folder, which is `docs/plans/` in these
examples. `plans_dir` in `.tandem.json` sets it.

```bash
tandem-handoff                          # the most recently reviewed plan, or else the newest .md
tandem-handoff docs/plans/specific.md   # or name one
```

With no argument, `tandem-handoff`, `tandem-review` and `tandem-status` first ask
`tandem gate latest-plan`. It reads the newest review record in `.tandem-relay/reviews/`
whose plan file still exists, wherever that plan lives. So tandem still finds a plan
reviewed outside `plans_dir`. With no such review, the commands pick the file with the
newest modification time, not by name, and don't look in subfolders.

That fallback makes **every `.md` file in the plans folder a candidate plan**. That is why
the placeholder there is `.gitkeep`, not `README.md`. Don't keep notes, templates or a
folder README beside real plans. If you edit one of them, it becomes the next handoff.

The builder reads a plan with none of the conversation that produced it. State the goal,
the constraints, and what "done" looks like. Say what the builder must not touch, because
it can't work out the scope from a conversation it never saw.

### What review will reject

The reviewer checks every plan before it is built (see
[step 3](walkthrough.md#3-the-reviewer-checks-the-plan)), and looks for five problems.
Writing the plan with them in mind costs less than answering them later.

1. **Ordering.** A task needs something that a later task adds.
2. **Contradiction.** Two tasks expect different things of one file, command output or
   state.
3. **Impossible step.** A step can't run as written. A task that must end with the check
   failing counts. tandem checks every task and stops the build when the check fails, so
   write failing tests in the same task as the code that makes them pass.
4. **Not possible for the builder.** A step needs authority the builder doesn't have.
   Commits are the usual case, so check whether the builder in this repo commits at all
   before you write a task that ends in one. The review prompt names the builder's kind and
   autonomy from `builder.json`. The reviewer runs in its own sandbox, so a test that
   sandbox refuses proves nothing about the builder. The reviewer reports such a test as
   "could not verify", not as an objection.
5. **Plan against its own evidence.** A test or example in the plan would not produce the
   result the plan claims.

## Which pane is the reviewer

`tandem-verdict` and `tandem-handback --review` record a review only from the reviewer
pane. Both ask `tandem agent self --expect NAME`, where NAME is the reviewer's recorded
name. tandem decides like this:

- With no `HERDR_PANE_ID`, the claimed role decides. That is `--role`, or else
  `HERDR_ROLE`, and it must be NAME.
- With `HERDR_PANE_ID` set, Herdr's name for the pane decides, whatever `--role` says. A
  builder that passes `--role reviewer` is still the builder, and tandem refuses it. tandem
  also refuses, and records and sends nothing, when Herdr names no agent in the pane, the
  lookup takes over 5 seconds, or Herdr fails in any other way.
- With `HERDR_PANE_ID` set but Herdr's socket out of reach (connection refused, permission
  denied or socket missing), the claimed role decides, and the command prints a warning.

The last case is for a sandboxed Codex reviewer. Codex runs its commands in a shared
app-server process, whose environment can carry another pane's `HERDR_PANE_ID` (F26 in
`guide/design.md`), and its sandbox blocks the Herdr socket. A builder without a sandbox
can always reach the socket, so Herdr always names it.

## When a review stands

**Every review starts in a fresh reviewer conversation, and a second review reads only the
change.** `tandem-review` resets the reviewer's conversation first. It uses the same
`tandem agent reset` the build gets, under the same rules, with the reset command for the
reviewer's kind.

`tandem gate scope` answers `diff` only when the reviewer wrote the earlier review, with the
same reviewer kind and model, and the same builder kind and autonomy. The reviewer then
gets the earlier review and a `diff -U5` of the reviewed plan against the plan as it is now.
It says whether each earlier objection is resolved, as `<review-id>#N`, and checks only the
changed text for the five problems. It doesn't read the whole plan again or run the check
command again.

A plan that is more than about half rewritten gets a full review. So does a changed
reviewer or builder, a review the builder wrote, or anything unexpected. On 2026-09-26,
before this rule, the second round cost 2.5M to 2.8M input tokens per feature, because it
neither started fresh nor read only the change.

**`tandem-handoff` refuses with exit 24 until a review clears.** A review is evidence about
one version of one plan, read by one reviewer, for one builder. Each of these voids it, and
the gate names which one:

| Reason | Means |
| --- | --- |
| `no_reviewer` | No reviewer is recorded now. tandem checks this first, whether or not a review exists |
| `plan` | The plan changed since its review |
| `builder_review` | The reviewer didn't write the review. Every review from before tandem required a reviewer counts as this |
| `reviewer` | The reviewer's kind changed, or its model changed when both reviews recorded one |
| `builder` | The recorded builder kind or autonomy changed |

The reviewer's effort and autonomy don't void a review. For every reason except
`no_reviewer`, `tandem-handoff` prints the reason and asks for a fresh review itself. For
`no_reviewer`, it names the two fixes, a `reviewer` block or `tandem-pair --reviewer KIND`,
and sends nothing.

The planner decides only one case: an objection that still stands. The planner can
overrule it, but has to say why:

```bash
tandem-handoff --override-review 20260910-204612-8821-19044 \
  --because "I commit in this repo, not you; skip the commit steps"
```

tandem sends the reason to the builder in the launch prompt, where it is the one
instruction that outranks the plan. tandem records the reason against the review, so the
ruling applies to every later handoff of that plan without retyping it. Editing the plan
voids the review and the ruling together.

`--override-review` needs `--because`. If a newer review has replaced the one you name,
tandem refuses with exit 24, records nothing, and names the newer review to read instead.

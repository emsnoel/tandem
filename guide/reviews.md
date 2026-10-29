# Plans and reviews

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

A plan is reviewed before it is built (see [step 3](walkthrough.md#3-the-reviewer-attacks-the-plan)), and
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

## Which pane is the reviewer

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

## When a review stands

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
`no_reviewer` it names the two remedies, a `reviewer` block or `tandem-pair --reviewer KIND`, and sends nothing. A standing objection
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

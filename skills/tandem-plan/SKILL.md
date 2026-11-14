---
name: tandem-plan
description: Write an implementation plan that tandem can review and build. Use when writing or editing a plan in a repo that has a .tandem.json, before running tandem-review or tandem-handoff.
---

# Writing a plan for tandem

tandem splits the plan into tasks and sends each one to the builder in a fresh
conversation. It runs the project's check after every task. A reviewer reads the
whole plan first, and a review round costs far more than writing the plan right.

## Where the plan goes

- Write one Markdown file in the plans folder. `tandem-project` prints the folder
  as `plans_dir`.
- Keep nothing else in that folder. With no argument, `tandem-handoff` takes the
  newest `.md` there, so a note or a README can become the next handoff.

## The opening

- Open with the goal, the constraints, what "done" looks like and what the builder
  must not touch.
- Keep the opening short. tandem repeats everything above the first task heading
  in every task prompt.

## Tasks

- Give each task a heading of the form `### Task 1: title`. Use `##`, `###` or
  `####`, and use the same level for every task. A plan with no task headings is
  built as one task, in one long conversation.
- The builder sees one task and none of this conversation. Name real paths and
  say exactly what to change.
- Each task must fit the project's `context_limit` together with the opening, so
  split a task that carries long code listings.

## What review rejects

1. Ordering. A task needs something a later task adds.
2. Contradiction. Two tasks expect different things of one file, command or state.
3. A step that cannot run as written. A task that ends with the check failing
   counts, because tandem stops the build on a failed check. Put a failing test in
   the same task as the code that makes it pass.
4. A step that needs authority the builder lacks. Commits are the usual case.
   Run `tandem-status` and read the builder's autonomy before you write a commit
   step. Leave commits out when you are not sure.
5. A test, command or example that would not give the result the plan claims.

## Optional task metadata

Put these lines directly under a task heading, with no blank line between:

```
### Task 2: Persist the receipt
Task-ID: receipt-store
Depends-On: event-envelope
Paths: internal/state
Outputs: receipt-schema
```

- `Task-ID` is one id made of lowercase letters, digits and hyphens.
- `Depends-On`, `Paths` and `Outputs` take comma-separated lists.
- tandem uses them to choose what a later task is shown from earlier ones.
- Spell the names exactly. A misspelt field stops the build before it starts.
- A plan with no metadata runs its tasks in order, each depending on the last.

## Then

Run `tandem-review FILE` and end your turn. tandem brings the verdict back to you.
When the review is clean, run `tandem-handoff FILE` and end your turn again.
tandem wakes you when the builder hands back or asks a question, so do not poll.

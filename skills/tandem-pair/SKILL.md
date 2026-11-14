---
name: tandem-pair
description: Run a tandem pair as the planner. Use in a repo that has a .tandem.json when you pair a builder, send a plan to review or to the builder, or get a message from tandem about a verdict, a handback or a question.
---

# Running a tandem pair

You are the planner. tandem runs a builder and a reviewer in other panes.

## The order

1. `tandem doctor`. Fix what it reports before you pair.
2. `tandem-pair`. It starts the builder and the reviewer.
3. `tandem-review FILE`. The tandem-plan skill covers how to write the plan.
4. `tandem-handoff FILE` once the review is clean.

## After a review or a handoff

End your turn. tandem wakes you with the verdict, the handback or a question. Do not
poll `tandem-status`. tandem types each message into your pane, so it waits while
you are busy.

## A relayed question

Write your answer to the exact path the message names, within 5 minutes. The asker
cannot see this conversation, so put everything it needs in the file.

## Overruling a review

When the reviewer objects and you are sure the objection is wrong, run
`tandem-handoff --override-review ID --because "TEXT"`. The builder reads the reason
in every task, so make it a clear instruction. If the objection is right, fix the
plan and run `tandem-review` again.

## When `tandem-pair` exits 23

A pane is at a dialog, such as a trust or hooks prompt. A person has to answer it in
that pane. Do not answer it, press keys in it, or work around it. Tell the user which
pane needs them, and run `tandem-pair` again once they say it is done.

---
name: tandem-pair
description: Run a tandem pair as the planner, in the pane a person works in. Use in a repo that has a .tandem.json when you pair a builder, send a plan to review or to the builder, or get a message from tandem about a verdict, a handback or a question. Not for a builder or reviewer that tandem started.
---

# Running a tandem pair

You are the planner. tandem runs a builder and a reviewer in other panes.

## The order

1. `tandem doctor`. Report what it finds. For a missing reviewer, pass
   `tandem-pair --reviewer KIND`. Edit `.tandem.json` only if the user asks.
2. `tandem-pair`. It starts the builder and the reviewer.
3. `tandem-review FILE`. The tandem-plan skill covers how to write the plan.
4. `tandem-handoff FILE` once the review is clean.

## After a review or a handoff

End your turn. tandem wakes you with the verdict, the handback or a question. Do not
poll `tandem-status`. tandem types each message into your pane once you are idle.

## A relayed question

Write your answer to the exact path the message names, within 5 minutes. The asker
cannot see this conversation, so put everything it needs in the file.

## Overruling a review

When the reviewer objects and you are sure the objection is wrong, run
`tandem-handoff --override-review ID --because "TEXT"`. The builder reads the reason
in every task. If the objection is right, fix the plan and review it again.

## What only a person decides

- `tandem-pair` exits 23 and names a pane: that pane is at a dialog. Do not answer it
  or press keys in it. Tell the user, and pair again once they say it is done.
- tandem asks to approve a kind (`--approve-kind`). Ask the user. Never pass it yourself.
- tandem says another pane holds your name. Ask the user which pane to free.

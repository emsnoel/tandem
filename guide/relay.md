# How the relay works

**Only a sandboxed builder uses the mailbox.** A `claude`, `copilot` or `opencode` builder
reaches the Herdr socket directly, so `tandem-ask` talks to the socket and never touches the
mailbox described here. The bridge still starts. It sleeps on file and Herdr events, waking
at least every 30 seconds (see [Commands](commands.md)), so it costs little. Guessing that it
isn't needed would risk a silent hang if the guess were wrong. A durable run needs the
bridge whatever the builder kind, because the bridge ticks the run.

Codex is the default builder. It runs under a seatbelt sandbox that blocks
`~/.config/herdr/herdr.sock`, so every Herdr command that talks to the server fails for it,
and tandem needs all of those commands. Codex can write inside its workspace, so tandem
passes messages through files there:

```
Codex          <repo>/.tandem-relay/              Claude
tandem-ask ──►  q/<id>.json  ──►  tandem-bridge ──prompt──►  planner
     │                                                        │
     └──── poll ◄──  a/<id>.md  ◄──── writes the answer file ──┘
```

`tandem-ask` tests the socket and picks direct or mailbox mode itself, so the same command
works from either agent. `tandem-handback` and `tandem-verdict` pick the same way, but
nobody waits for an answer. The sender queues its finished message and exits, and the bridge
delivers it word for word. If delivery fails, the bridge leaves a `<id>.UNDELIVERED` marker
beside what it tried to send: the report for a handback, or the stored review for a verdict.

A handback travels the same way, with `"kind": "handback"` on the queued file. The builder
has finished, so nothing polls for a reply. The bridge delivers the message the builder
wrote, and leaves an `.UNDELIVERED` marker beside the report if it can't. Only
`tandem-handback` holds the message text, so the sandboxed and direct paths can't drift
into saying different things.

**The author of an answer or report writes it to a file.** tandem never scrapes it from a
pane. The terminal collapses long output (`… +N lines`), and a scraped answer would lose the
collapsed part.

## Safety properties

Keep these true if you edit tandem:

- **Never write raw bytes to a blocked pane.** They would type into another agent's
  approval dialog. `tandem-ask` waits for the block to clear instead.
- **`agent prompt` returning 0 doesn't prove delivery.** Herdr reports `idle` for an agent
  sitting at a native dialog it can't see, and the dialog swallows the input.
  `tandem-handoff` checks that the builder actually left `idle`.
- **Teardown is always deliberate, and always checked.** `herdr pane close` deletes the
  pane's scrollback. Only `tandem-unpair` closes the builder, and only the planner runs it.
  It refuses unless the work was handed back and signed off, and the project's check
  command exits 0. A legacy build is signed off by report id, and a durable run by each
  task's recorded check and review. `tandem-unpair` runs the check itself before it closes
  anything.
- **The launch prompt and handback templates are quoted heredocs, filled by parameter
  substitution.** `prompt_extra` is any text from whatever repo you point tandem at.
  Through an unquoted heredoc, a backtick in a manifest would run on your machine.
- **tandem refuses an adapter mapping that doesn't match what you asked for, and never
  approximates it.** Widening grants more authority, and needs `allow_widen`, which only
  your user config can set. Narrowing withholds authority you asked for, and needs
  `allow_degrade`.

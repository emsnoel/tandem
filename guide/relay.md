# How the relay works

**Only a sandboxed builder needs any of this.** A `claude`, `copilot` or `opencode` builder reaches
the Herdr socket directly, so `tandem-ask` takes the socket path and the mailbox below is
never touched. The bridge still starts. It sleeps on file and Herdr events with a 30-second
backstop (see [Commands](commands.md)), so it costs little, and predicting that it is unneeded
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

# When tandem refuses

Every refusal prints the command or edit that fixes it, so the message is usually all you
need. This table lists each exit code for reference.

`tandem doctor` finds the causes of exits 3, 20, 21, 22 and 23 before you pair, and
`tandem-pair` runs it first. See [Checking your setup](commands.md#checking-your-setup).

| Exit | Means | Fix |
| ---- | ----- | --- |
| 3 | This shell can't reach the Herdr socket, so nothing was delivered | Check the Herdr server is running, and run the command from a pane Herdr manages |
| 4 to 9 | A teardown gate refused | See [step 7](walkthrough.md#7-the-builder-hands-back-and-the-planner-closes-the-pair) |
| 7 | From `tandem-review` or `tandem-handoff`, tandem typed the prompt but the pane never started working. The usual cause is a native dialog Herdr can't see | Clear the dialog in the named pane and run the command again |
| 8 | From `tandem-review` or `tandem-handoff`, the pane started a turn that ended within 8 seconds with no new tokens in the conversation's log, so the model did no work. A model the CLI rejects, an expired login or a used-up quota looks like this. The message shows the pane's last 20 lines | Fix what the pane shows, or run `tandem-pair` again with another model. Then run the command again |
| 20 | The value would have to narrow. The adapter can't give the builder the authority you asked for, and the pane would stall | Pick a supported value, or add the axis to `allow_degrade` in the repo manifest. `tandem paths manifest` prints the manifest's path |
| 21 | The value would have to widen. The adapter would grant authority you did not ask for | Pick a supported value, or add the axis to `allow_widen` in your user config |
| 22 | The repository asked for an agent `kind` you haven't approved for that repository | Run `tandem-pair --approve-kind <kind>` once |
| 23 | The builder or reviewer would start at a prompt that needs a person. `tandem build wait` and `tandem pane wait-shell` also exit 23 when their timeout passes first. A legacy `tandem build start` exits 23 when `reviewer.json` records no reviewer pane | Clear the prompt. Usually that means running the agent once in the folder and accepting its trust dialog. For a missing reviewer, declare a `reviewer` block and run `tandem-pair` again, or run `tandem-pair --reviewer KIND` |
| 24 | The plan has not passed review. One of these is true: there is no reviewer (tandem checks this first), the plan has no review, the plan changed since its review, the recorded builder changed, the reviewer didn't write the review, the reviewer's kind or model changed, or the reviewer's objections stand. Overruling a review that a newer one replaced also exits 24, and the message names the newer one | Run `tandem-review <plan>`, fix the plan, or run `tandem-handoff --override-review <id> --because "..."`. If the message names a newer review, read it first |
| 25 | From `tandem agent prompt --as`, the prompt never started a turn. The scripts report this as their own 7 | As for 7 |
| 26 | From `tandem agent prompt --as`, the turn ended at once with no model work. The scripts report this as their own 8 | As for 8 |
| 27 | From `tandem agent alive`, an agent quit right after it started: Herdr stopped listing it, or its pane is back at the shell. From `tandem agent watch`, a watched agent was released or its pane closed, or it was already gone when the watch started. `tandem-pair` runs this check after starting each agent, and stops with the pane's last lines | Fix what the pane shows, then run `tandem-pair` again |
| 30 | `tandem doctor` found at least one problem that would stop a pairing. `tandem-pair` runs doctor first, and stops with its code | Fix each `FAIL` line as its `fix:` line says, or run `tandem-pair --skip-doctor` |

## A pane showing a subagent or a list

Herdr reports a pane's status, but not which conversation its input reaches. Each agent CLI
has screens where Herdr reports `idle` (or `working`) but typed text misses the main
conversation. Once, the planner pane showed a background subagent's screen, and the
bridge's verdict reached that subagent nine minutes later instead of the planner. We
checked each of these screens live on 2026-10-17:

| Kind | Screen | What typed text does there |
| ---- | ------ | -------------------------- |
| Claude Code | a subagent's screen (agent list, then Enter) | goes to that subagent |
| Claude Code | the agent list holding the keys (`↑/↓ to select`) | stays in the input, unsent |
| Codex | a sub-agent's thread (`⌥←` / `⌥→`) | stays in the input, which is disabled |
| Codex | the agent command center (`←`, `/agent`) | Enter opens another session |
| Copilot | a subagent's screen (`/tasks`, then Enter) | goes to that subagent, which answers it |
| Copilot | the task list, the focused sidebar, the Sessions tab | single keys act, and `x` kills a task or closes a session |
| Copilot | a GitHub tab (Issues, Pull requests, Gists) | goes nowhere |
| opencode | a child session (`ctrl+x down`) | is dropped, because child sessions can't take prompts |

Before typing, `tandem agent prompt`, a durable run's send and `tandem agent reset` read the
pane's screen (`internal/agent/screen.go`). They type nothing into one of these screens.
The refusal is `agent_input_elsewhere` (exit 10), and its message names what the pane shows.
What happens next depends on the sender:

- `tandem-handback` and `tandem-ask` exit 3.
- The bridge logs the failure and leaves an `.UNDELIVERED` marker with the reason.
- A durable run tries again on its next tick.
- A reset reports `skip:off-main`.

Return the pane to its main conversation and run the command again. Don't press Esc to
leave a Claude subagent's screen while its input has focus, because Esc there interrupts
the subagent. Use the agent list instead: press `↓`, pick `main`, and press Enter.

## Why is it using that model?

A value can come from a flag, the environment, `.tandem.json`, your user config, or an
adapter default, in that order. Every value reports where it came from, so ask instead of
guessing:

```bash
tandem config resolve --role builder --root .
```

```json
{"kind":{"value":"codex","from":"repo"},"model":{"value":"gpt-5.4","from":"env"}, ...}
```

`from: "adopted"` means tandem adopted a running agent instead of launching one. The value
then describes what the agent is actually running with, and the configured value is not in
force.

The reviewer resolves on its own, and never from a builder source:

```bash
tandem config resolve --role reviewer --root .
```

It always exits 0 and reports one of three outcomes in `status`:

- `none` means there is no `reviewer` block and no `--kind`.
- `resolved` carries the values and where each came from, as for the builder.
- `invalid` carries the reason, such as a block with no `kind`.

It takes `--kind`, `--model`, `--effort` and `--autonomy` as overrides, the same way
`tandem-pair` passes its `--reviewer*` flags.

# When tandem refuses

Every refusal prints the exact command or edit that resolves it, so the message is
usually enough on its own. The codes, for reference:

| Exit | Means | Fix |
| ---- | ----- | --- |
| 3 | the Herdr socket is unreachable from this shell — nothing was delivered | check the Herdr server is running, and run from a Herdr-managed pane |
| 4–9 | a teardown gate refused | see [step 7](walkthrough.md#7-the-builder-hands-back-and-the-planner-closes-the-session) |
| 7 | from `tandem-review` or `tandem-handoff`: the prompt was typed but the pane never started working, usually at a native dialog Herdr cannot see | clear the dialog in the named pane and re-run |
| 8 | from `tandem-review` or `tandem-handoff`: the pane started a turn and it ended within 8 seconds with no new tokens in the conversation's log, so the model did no work. A model the CLI rejects, an expired login or used-up quota looks like this. The message shows the pane's last 20 lines | fix what the pane shows, or re-run `tandem-pair` with another model, then re-run the command |
| 20 | the value would have to **narrow** — the adapter cannot give the builder the authority you asked for, and the pane would stall | pick a supported value, or add the axis to `allow_degrade` in the repo manifest — `tandem paths manifest` names it |
| 21 | the value would have to **widen** — the adapter would grant authority you did **not** ask for | pick a supported value, or add the axis to `allow_widen` in your *user* config |
| 22 | the repository asked for an agent `kind` you have not approved **for that repository** | `tandem-pair --approve-kind <kind>`, once |
| 23 | the builder or reviewer would launch into a gate that needs a human. Also from `tandem build wait` and `tandem pane wait-shell` when the timeout passes first, and from a legacy `tandem build start` when `reviewer.json` records no reviewer pane | clear the gate. Usually that means running the agent once in the directory and accepting its trust dialog. For a missing reviewer, declare a `reviewer` block and re-run `tandem-pair`, or run `tandem-pair --reviewer KIND` |
| 24 | the plan has not cleared review — there is no reviewer (checked first, whether or not a review exists), no review exists for it, the plan changed since it was reviewed, the recorded builder identity changed, the review was not written by the reviewer, the reviewer's kind or model changed since, or the reviewer's objections stand. Overruling a review that a newer one has since superseded refuses the same way, naming the newer one | `tandem-review <plan>`, fix the plan, or `tandem-handoff --override-review <id> --because "..."` — read the newer review first if you are told it has superseded the one you named |
| 25 | from `tandem agent prompt --as`: the prompt never started a turn. The scripts report this as their own 7 | as for 7 |
| 26 | from `tandem agent prompt --as`: the turn ended at once with no model work. The scripts report this as their own 8 | as for 8 |
| 27 | from `tandem agent alive`: an agent that was just started quit, so Herdr stopped listing it or its pane is back at the shell. From `tandem agent watch`: a watched agent was released or its pane closed, or it was already gone when the watch subscribed. `tandem-pair` runs this after starting each agent and stops with the pane's last lines | fix what the pane shows, then re-run `tandem-pair` |

## A pane showing a subagent or a list

Herdr reports a pane's status, not which conversation its input reaches. Each agent CLI
has screens where Herdr reads `idle` (or `working`) but typed text misses the main
conversation. Once, the planner pane showed a background subagent's screen, and the
bridge's verdict reached that subagent nine minutes later instead of the planner. The
screens, each checked live on 2026-10-17:

| Kind | Screen | What typed text does there |
| ---- | ------ | -------------------------- |
| Claude Code | a subagent's screen (agent list, then Enter) | goes to that subagent |
| Claude Code | the agent list holding the keys (`↑/↓ to select`) | stays in the input, unsent |
| Codex | a sub-agent's thread (`⌥←` / `⌥→`) | stays in the input; input is disabled |
| Codex | the agent command center (`←`, `/agent`) | its Enter opens another session |
| Copilot | a subagent's screen (`/tasks`, then Enter) | goes to that subagent, which answers it |
| Copilot | the task list, the focused sidebar, the Sessions tab | single keys act: `x` kills a task or closes a session |
| Copilot | a GitHub tab (Issues, Pull requests, Gists) | goes nowhere |
| opencode | a child session (`ctrl+x down`) | dropped; child sessions cannot be prompted |

Before typing, `tandem agent prompt`, a durable run's send and `tandem agent reset` read
the pane's screen (`internal/agent/screen.go`) and type nothing into one of these. The
refusal is `agent_input_elsewhere` (exit 10), and its message names what the pane
shows. `tandem-handback` and `tandem-ask` exit 3, the bridge logs the failure and leaves
an `.UNDELIVERED` marker carrying the reason, a durable run tries again on its next
tick, and a reset reports `skip:off-main`. Return the pane to its main conversation and
re-run. Don't press Esc to leave a Claude subagent's screen with its input focused: Esc
there interrupts the subagent. Use the agent list (`↓`, pick `main`, Enter).

**Why is it using that model?** A value can come from a flag, the environment,
`.tandem.json`, your user config, or an adapter default — in that order. Ask rather than
guess; every value reports where it came from:

```bash
tandem config resolve --role builder --root .
```

```json
{"kind":{"value":"codex","from":"repo"},"model":{"value":"gpt-5.4","from":"env"}, ...}
```

`from: "adopted"` means the agent was adopted rather than launched, so that value
describes what it is *actually* running with — the configured one is not in force.

The reviewer resolves on its own, and never from a builder source:

```bash
tandem config resolve --role reviewer --root .
```

It always exits 0 and reports one of three outcomes in `status`. `none` means no `reviewer`
block and no `--kind`. `resolved` carries the values with provenance, as for the builder.
`invalid` carries the reason, such as a block with no `kind`. It takes `--kind`, `--model`,
`--effort` and `--autonomy` as overrides, as `tandem-pair` passes its `--reviewer*` flags.

# Durable runner rollout report

Filled from the token eval's report template on 2026-10-03 from runs made in this
session. Every figure below was produced by the command beside it. The conformance matrix and
the scenario table come from fixtures and a fake Herdr. The live table comes from one smoke run
per kind against the real CLIs; the cost and latency section has no live figures.

This is a record of 2026-10-03 and is left as it was. What the code has changed since, and
which findings and gates it touches, is in [Since this report](#since-this-report) at the end.

## Environment

- Date, commit: 2026-10-03, base commit 1a5d6bc with Tasks 01-28 uncommitted on top
- OS and architecture: macOS 26.6.2, arm64
- Herdr version: 0.8.2 (the binary on PATH; the fixtures use a fake `herdr`, not this one)
- Go, Python: go1.27.0 (go.mod floor 1.26), Python 3.14.7
- Agent CLI versions: codex-cli 0.159.2 (0.160.0 for the re-run), Claude Code 2.1.288, Copilot CLI 1.0.90, OpenCode 1.18.32. Models were each CLI's own default, except OpenCode, which was switched by hand to GPT-5.5 after its default endpoint failed.

## Correctness gates

| Gate | Command | Exit code | Result |
|---|---|---|---|
| Unit and package tests | `go test ./...` | 0 | all packages ok |
| Race, touched packages | `go test -race ./internal/controller ./internal/state ./internal/snapshot ./internal/evalrow ./cmd/tandem` | 0 | all ok |
| Whole check | `HERDR_TRIAGE_SKIP=1 ./tests/run.sh` after an isolated install | 0 | "all checks passed" |

## Four-builder conformance (fixture, fake Herdr)

`bash tests/test-durable-builders.sh`: exit 0, 74 checks passed, 0 failed. This proves
compatibility against a fake Herdr only.

```
== matrix (fixture results; live verification: none, every kind UNVERIFIED live) ==
  codex     durable  launch                 pass
  codex     durable  fresh task             reset, then prompt
  codex     durable  start ack              pass
  codex     durable  submit                 pass
  codex     durable  check                  pass
  codex     durable  pane review            refused until settled
  codex     durable  repair                 new attempt
  codex     durable  stale event            quarantined
  codex     durable  restart                nothing resent
  codex     durable  usage unavailable      unknown, not zero
  codex     durable  stop                   released after idle
  claude    durable  launch                 pass
  claude    durable  fresh task             reset, then prompt
  claude    durable  start ack              pass
  claude    durable  submit                 pass
  claude    durable  check                  pass
  claude    durable  pane review            refused until settled
  claude    durable  repair                 new attempt
  claude    durable  stale event            quarantined
  claude    durable  restart                nothing resent
  claude    durable  usage unavailable      unknown, not zero
  claude    durable  stop                   released after idle
  copilot   durable  fresh task (strict)    refused, nothing typed
  copilot   durable  launch                 pass
  copilot   durable  fresh task             shared (--share-context), no reset
  copilot   durable  start ack              pass
  copilot   durable  submit                 pass
  copilot   durable  check                  pass
  copilot   durable  pane review            refused until settled
  copilot   durable  repair                 new attempt
  copilot   durable  stale event            quarantined
  copilot   durable  restart                nothing resent
  copilot   durable  usage unavailable      unknown, not zero
  copilot   durable  stop                   released after idle
  opencode  durable  fresh task (strict)    refused, nothing typed
  opencode  durable  launch                 pass
  opencode  durable  fresh task             shared (--share-context), no reset
  opencode  durable  start ack              pass
  opencode  durable  submit                 pass
  opencode  durable  check                  pass
  opencode  durable  pane review            refused until settled
  opencode  durable  repair                 new attempt
  opencode  durable  stale event            quarantined
  opencode  durable  restart                nothing resent
  opencode  durable  usage unavailable      unknown, not zero
  opencode  durable  stop                   released after idle
  codex     legacy   launch                 pass
  codex     legacy   fresh task             adapter reset
  codex     legacy   submit + check         pass
  codex     legacy   stop                   recorded
  codex     legacy   restart                resumes at 2
  claude    legacy   launch                 pass
  claude    legacy   fresh task             adapter reset
  claude    legacy   submit + check         pass
  claude    legacy   stop                   recorded
  claude    legacy   restart                resumes at 2
  copilot   legacy   launch                 pass
  copilot   legacy   fresh task             no verified reset, shared
  copilot   legacy   submit + check         pass
  copilot   legacy   stop                   recorded
  copilot   legacy   restart                resumes at 2
  opencode  legacy   launch                 pass
  opencode  legacy   fresh task             no verified reset, shared
  opencode  legacy   submit + check         pass
  opencode  legacy   stop                   recorded
  opencode  legacy   restart                resumes at 2
```

## Live verification

A live smoke was run on 2026-10-03 with one two-task plan per kind, in a throwaway repo, in
its own Herdr tab, with the controller driven by hand (`tandem build tick`, `recover`, `stop`)
and the pane reviews read from the pane and recorded with `tandem build review-record`.
Herdr 0.8.2, macOS 26.6.2 arm64, tandem built from this tree. One run per kind (Codex twice), so one
sample each: this is a smoke, not a measurement.

| Kind (CLI version) | Launch | Fresh task | Ack | Submit + check | Pane review | Stop | Restart | Usage |
|---|---|---|---|---|---|---|---|---|
| Codex (codex-cli 0.159.2) | pass | pass (reset, then prompt) | pass | pass | pass (task 1 accepted) | pass | pass | **defect found** (below) |
| Codex re-run (codex-cli 0.160.0, with the fixes) | pass | pass | pass | pass | pass (both tasks accepted, run not stopped) | not re-run | not re-run | **pass**, matches the logs exactly (below) |
| Claude (2.1.288) | pass | pass (reset, then prompt) | pass | pass | pass (task 1 accepted) | pass | pass | unavailable, as designed |
| Copilot (1.0.90) | pass, strict refusal typed nothing | shared (`--share-context`) | pass | pass | pass (task 1 accepted) | pass | pass | unavailable, as designed |
| OpenCode (1.18.32) | pass at autonomy `full` | shared (`--share-context`) | **needed a manual nudge** | pass | pass (task 1 accepted) | pass | pass | unavailable, as designed |

What "pass" means here: the builder ran `tandem-handback ... --started` and `--report` itself
from the prompt tandem typed; tandem's check ran and passed; the review prompt was typed into
the builder's pane and its JSON reply, once the pane settled, was recorded and accepted task 1;
`build stop` while the builder was working went to `stopping` and then `paused` only after the
pane was seen idle; two `build recover` calls changed nothing in the pane (its state-change
counter was identical before and after). Codex, Claude, Copilot and OpenCode were
exercised, one run each (Codex twice). Nothing else about any kind is verified.

Not exercised live: finishing task 2 and the final planner notice (every run was stopped
while the builder was on task 2), a repair attempt, budgets, a changes-requested review, a
crash of the controller process mid-send, and direct reviewer execution (disabled).

Findings, all from the live runs:

1. **Codex usage failed; fixed and confirmed live.** `tandem build usage` returned
   "conflicting update: source ... already recorded with different figures". Herdr reports a
   Codex session id only after the new conversation has made a model call, so the prompt
   receipt recorded after a reset carried the conversation that had just been left, and the
   second task's job claimed the first task's log lines. Fixes, each with a failing test first
   and a mutation check: a receipt now names the new conversation or none
   (`TestHerdrReceiptNamesTheNewConversationOrNone`), and a log line named by two jobs is
   counted once and noted instead of failing the report
   (`TestUsageLogNamedByTwoJobsIsCountedOnce`). A job whose session cannot be established now
   reports usage as unavailable, never as another job's figures. The fix was then run
   against a live Codex 0.160.0 over a full two-task plan, both tasks accepted: `tandem build usage`
   returned complete figures from 13 sources with no conflict, input 321,079, cached 273,280,
   output 2,037, which equals the sum of the final `total_token_usage` of the two rollout files
   of that run exactly. The review tokens are inside the builder figure, because reviews run in
   the builder's session, and the report says so; a reviewer figure is unavailable.
2. **The pane review is the builder's own conversation.** The review prompt went to the same
   pane the builder worked in, so the agent that wrote the code also reviewed it. The review
   result is bound to the job, patch and tree, but the reviewer is not independent. This is the
   designed fallback; independent review needs the direct reviewer capability, which is off.
3. **Copilot's reply is cut off by the default pane read.** The review JSON was truncated at the
   terminal width. `herdr agent read --source recent-unwrapped` returns the whole line. The
   operator procedure now says so; tandem does not yet read the reply itself.
4. **A provider failure is silent.** OpenCode's default model returned "Endpoint is
   unavailable" and the pane went idle without acknowledging. The run stayed `running` with the
   task `pending` (the ten-minute acknowledgement timeout had not passed), and nothing said the
   model had failed. After the model was switched in the pane the prompt had to be re-asked by
   hand, because tandem types a prompt once.
5. **Claude was `blocked` for part of its first turn.** `herdr agent get` showed `blocked`
   about eight seconds in, and it went idle by itself. The cause was not identified.
6. **Stop does not interrupt a turn.** On Claude and Copilot the builder finished task 2's file
   while the run was `stopping`; the run paused when the pane went idle. That is the designed
   behaviour, and it means a stop can take as long as the builder's current turn.

7. **Herdr showed `idle` over a Codex startup dialog.** On the re-run Codex first stopped at
   an update prompt and then a "cannot use the background server" prompt (the update was accepted
   by the user, and the second was answered "run without daemon this time"). Herdr reported the
   pane as `idle` while a dialog was up, so tandem typed the task prompt into it. The run paused
   as `writer_unknown` and sent nothing further, which is the designed safe result, but the
   operator has to look at the pane. Launch Codex to its prompt before starting a run.

Configuration touched: tandem's launch gate wrote Codex and Copilot trust entries for the
throwaway repos, and Claude Code added a project entry for its repo to `~/.claude.json`.
All three were removed afterwards (`tandem trust clean`, and one project key deleted from
`~/.claude.json`). The throwaway repos, the isolated state home and the panes were deleted.

## Cost and latency

Not measured. The token eval's fixture mode (exit 0) used scripted
agents, so its token counts are the scripted logs' and its latency is a fixture clock. It
says nothing about real cost. What it does show is orchestration outcomes over the nine
scenarios, one pair each:

| Scenario | Baseline (legacy engine) | Durable (controller) |
|---|---|---|
| chain-20 | 20 accepted, 0 failed tasks, 0 failed runs | 20 accepted, 0 failed tasks, 0 failed runs |
| dag-20 | 20 accepted, 0 failed tasks, 0 failed runs | 20 accepted, 0 failed tasks, 0 failed runs |
| large-required | 0 accepted, 0 failed tasks, 0 failed runs | 0 accepted, 1 failed tasks, 1 failed runs |
| legacy-plan | 3 accepted, 0 failed tasks, 0 failed runs | 3 accepted, 0 failed tasks, 0 failed runs |
| partial-usage | 2 accepted, 0 failed tasks, 0 failed runs | 2 accepted, 0 failed tasks, 0 failed runs |
| red-repair | 1 accepted, 0 failed tasks, 0 failed runs | 1 accepted, 0 failed tasks, 0 failed runs |
| send-crash | 0 accepted, 0 failed tasks, 0 failed runs | 0 accepted, 1 failed tasks, 1 failed runs |
| shared-dirty | 2 accepted, 0 failed tasks, 0 failed runs | 2 accepted, 0 failed tasks, 0 failed runs |
| tiny-2 | 2 accepted, 0 failed tasks, 0 failed runs | 2 accepted, 0 failed tasks, 0 failed runs |

`large-required` and `send-crash` have no baseline row because the legacy engine cannot
express them. No paired live run was made, so there is no sample, no uncached figure for
the baseline (the legacy usage has no cached counter) and no p95.

## Triage skipping

No live traffic. The calibration report has no rows from a real run, and skip stays off.

## Decision

- Default engine: **stays legacy**. Durable remains opt-in (`TANDEM_BUILD_ENGINE=durable`).
- Triage skip default: **off**.
- Gates not met: all four builders verified live (one smoke run each, task 1 only; no repair, no
  full plan, no controller crash, the Codex usage fix is confirmed live over one plan); recovery gates against a real
  Herdr beyond stop and `recover` (none); measured cost targets (not measured, no live pairs); direct reviewer
  execution (disabled for every kind, awaiting measured evidence per CLI version); usage
  readable only for Codex; a review reply is not read from the pane automatically; a plan
  edited mid-run is not reconciled by any command.
- Next step: add a full-plan run with a repair per kind, then at least ten paired runs per main
  scenario with `run.sh --live --max-input-tokens N`, and fill a fresh copy of the template.

## Since this report

Checked against the code on 2026-10-18 (`main` at 526d4d9). Nothing below was re-measured
live for this report. Each item says what the code does now, not that it was verified on a
real CLI.

- **The decision stands.** The default engine is still legacy. A build is durable only by
  `TANDEM_BUILD_ENGINE=durable` or `"build_engine": "durable"` in `.tandem.json`.
  Triage skip is still off. No paired live run has been made, so cost and latency are still
  not measured.
- **Finding 2: an independent reviewer now exists.** `tandem-pair --reviewer KIND` starts a
  reviewer pane on its own model, and every review prompt of a durable run goes there. Its
  passing review is the independent check, and a result from the builder's or planner's
  conversation pauses the run with `verification_identity`. Without a reviewer pane the
  finding still holds. Direct reviewer execution is still disabled for every kind.
- **Finding 3: tandem now reads the review reply itself.** Each tick reads the settled
  pane unwrapped (`--source recent-unwrapped`), takes the last JSON object that names the
  open review job, and records it through the same path as `review-record`. This meets the
  gate "a review reply is not read from the pane automatically" in code.
- **Finding 4, in part: a turn with no model work is caught.** A task or review prompt
  whose turn ends within 8 seconds with no new tokens is recorded as not sent and retried.
  After 3 such sends the run pauses as `dispatch_failed` with the pane's last lines. A
  provider failure that leaves the turn running is still silent until the timeout.
- **Usage.** `tandem build usage` reads Codex, Claude and Copilot builders. A Codex
  conversation is found by the task's nonce in its logs, never from Herdr. OpenCode
  usage is still unavailable, so a configured budget pauses it.
- **Waiting moved to events.** The bridge, prompt confirmation and resets wait on
  Herdr 0.9.3's socket event stream and on mailbox file events, with a 30-second backstop,
  instead of polling. The live runs above used Herdr 0.8.2, which has no such stream.
- **Typing into the wrong screen is refused.** A send, a reset or a prompt types
  nothing into a pane showing a subagent or a list (`agent_input_elsewhere`), and a durable
  run tries again on its next tick.
- **Gates still not met:** a full plan with a repair on every kind, a controller crash
  during a send on a real Herdr, measured cost targets, direct reviewer execution, and a
  command that reconciles a plan edited mid-run.

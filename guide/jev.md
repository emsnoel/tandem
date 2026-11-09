# Optional Jev features

Staffing advice is off by default. It calls Jev, a TypeSafe model, and needs a
`TYPESAFE_API_KEY` in the environment.

## Staffing advice

`tandem decision assess --plan FILE --root DIR --render` asks Jev to classify a plan and prints
a short block marked as advice. A run doesn't add this block to its task prompt yet. The block says what kind of work it is and whether it looks like
it can run in parallel. For builders that can choose a model per subagent, it also names
the lowest level to hand the work to.

The builder may ignore all of it. The advice can't change a review verdict, widen
authority, change a launch argument or affect teardown. A test forbids `internal/gate`
from importing `internal/decision`, so a judgement can't reach a gate even by accident.

```bash
HERDR_DECISION=1 tandem-handoff                     # ask for advice on this handoff
tandem decision assess --plan P --root R --dry-run  # show what would be sent, and send nothing
```

Without the key, or if the call fails or times out, tandem adds nothing, and the prompt is
exactly what it would have been. `tests/test-decision.sh` checks that byte for byte against
the real script. tandem reads the key only from the environment, never from `.tandem.json`,
because `.tandem.json` is cloned with the repository.

**The handoff says what happened.** The prompt looks the same whether nobody asked Jev, or
Jev answered and chose not to advise. So `tandem-handoff` prints one line either way,
outside the prompt:

```
staffing advice: added to the launch prompt (Jev).
staffing advice: none (Jev answered but was not confident enough about this plan to advise).
staffing advice: off (HERDR_DECISION is not 1).
WARNING staffing advice: HERDR_DECISION=1 but TYPESAFE_API_KEY is not set; Jev was not asked.
WARNING staffing advice: the Jev call failed (<reason>); none added.
```

"None" is the usual answer for a plan Jev can't classify with confidence. An uncertain
assessment adds nothing, on purpose, instead of a guess. `tandem decision assess --status FILE`
reports it. Check which shell the handoff runs in. A planner started before you exported
`HERDR_DECISION` doesn't have the variable, and the line will say `off`.

`claude` and `codex` builders can choose a model for each subagent. A `claude` agent
definition has a default `model:`, and the parameter passed when the subagent starts
overrides it. For `codex`, we checked codex-cli 0.155.1 (`multi_agent_version` v2) by
reading a real session's rollout log. `spawn_agent` takes `model` and `reasoning_effort`,
as well as `{task_name, fork_turns, message}`, and uses both. A parent running `gpt-5.6-sol`
at medium started children that ran `gpt-5.6-luna` at medium and `gpt-5.6-terra` at high.
Codex still doesn't read project-level `.codex/agents/*.toml`, so the advice can choose a
model level, but not a named agent definition.

Nobody has checked `copilot`, so tandem treats it as unable to choose. For
that builder the advice still describes the work, but doesn't name a level.

> An earlier version of this section said the opposite about `codex`, citing the same CLI
> version. It was wrong when written, not made wrong by a later release.
> `internal/agent/subagent_test.go` now ties the claim to the evidence.

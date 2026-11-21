# Notes for agents working on tandem

tandem gives a builder this file in every task (`conventions_file` defaults to
`AGENTS.md`), and Codex loads it at the start of every conversation.
`guide/contributing.md` has the rules every test follows. This file covers how
to test while tandem builds tandem.

## Testing during a build

tandem runs the full check, `./tests/run.sh`, after every handback, outside
any sandbox. So:

- **Don't run the full check yourself.** Run the tests for what you changed,
  then hand back. tandem's own check is what accepts the task.
- **Run the plan's named tests first.** If a task names none, use the table
  below.
- **Use `--only` for targeted runs.** It sets up the same environment as the
  full check, then runs only what you name:
  `./tests/run.sh --only ./internal/agent/... --only test-kind-drift`.
- **Set `TANDEM_BUILD_CHECK=1` for any run by hand.** Without it,
  `test-staleness` fails, because you can't reinstall tandem (#103).

| You changed | Run |
|---|---|
| a Go package | `--only ./<that package>/...` |
| `bin/` or `bin/lib/` | `--only` each suite that names it: `rg -l '<file name>' tests/` |
| a `tests/test-*.sh` suite | `--only <suite name>` |
| a list of agent kinds | `--only ./internal/agent/... --only ./cmd/tandem/... --only test-kind-drift --only test-discontinued-kinds` |
| `README.md` or `guide/` | `--only test-kind-drift` if a kind list changed, otherwise nothing |

Don't run a suite with plain `bash tests/test-x.sh`. `run.sh` first sources
`tests/lib/clean-env.sh`, which points `HERDR_SOCKET_PATH` at a socket that
doesn't exist and moves `TANDEM_SKILL_HOMES` to a scratch folder. Without it, a
suite can reach your real Herdr panes or overwrite your real skill links.

## Inside a Codex sandbox

The shell suites that use the fake Herdr socket fail inside a Codex builder's
sandbox with `fake socket did not start`, because the sandbox won't let the
server open a Unix socket (#106). If that is the only failure, it isn't yours
to fix. Say so in the handback and hand back as done, not stuck. tandem's check
runs outside the sandbox.

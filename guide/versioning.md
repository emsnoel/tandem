# Versioning

## Scheme

Tandem follows [semantic versioning](https://semver.org). A release is a git tag `vX.Y.Z`.
A tag with a suffix, such as `v0.2.0-rc.1`, is a pre-release. GitHub marks it as one, and
`install.sh` without `--version` never installs it.

## What is breaking for tandem

A change is breaking when a setup that works today refuses, or behaves differently, and the
user has to edit something to get back to where they were. For tandem that means any of
these:

- removing or renaming a command (`tandem-*` or a `tandem` subcommand) or a flag;
- changing what an exit code means, or which exit code a refusal uses;
- removing or renaming a `.tandem.json` key or a user-config key (`~/.config/herdr/tandem.json`),
  or changing its default;
- a durable state schema migration (`internal/state/migrations/`) that an older tandem cannot
  read;
- changing the relay protocol (`ProtocolVersion`, currently 2, in
  `internal/controller/rundir.go`) or the format of a mailbox file.

## Features and fixes

A new command, flag, config key, agent kind or exit code is a feature. A change that makes
existing behaviour match what the docs already say is a fix. A change that makes a refusal
fire where it should have fired all along is a fix, even when someone's setup now refuses.
Say so in the commit body.

## How the version is chosen

Nobody picks the number. release-please reads the commit subjects merged to `main` since
the last release:

| Commit | Bump |
| ------ | ---- |
| `fix: ...` | patch |
| `feat: ...` | minor |
| `feat!: ...`, `fix!: ...`, or a `BREAKING CHANGE:` footer | major |
| `refactor`, `docs`, `ci`, `test`, `chore` | none; no release on its own |

While the major version is 0, a breaking change bumps the minor number instead
(`bump-minor-pre-major` in `release-please-config.json`). A `BREAKING CHANGE:` footer says
what the user must do, because it becomes that release's changelog entry.

A commit subject with a typo in its type (`feta:`) does not count as a feature. Check the
release PR's changelog before merging it.

## Pre-releases

Cut one when a change needs testing on someone else's machine before it is the default for
everyone: `gh workflow run release.yml --ref main -f prerelease_tag=v0.2.0-rc.1`. Testers
install it with `install.sh --version v0.2.0-rc.1`. See [releasing.md](releasing.md).

## Going to 1.0

Tandem goes to 1.0 when the durable engine is the default and the exit codes and
`.tandem.json` keys are considered stable. Until then, read every minor release's changelog
before upgrading.

# Install

```bash
curl -fsSL https://raw.githubusercontent.com/emsnoel/tandem/main/install.sh | bash
```

This installs the latest release. You need `curl`, `tar`, `jq` and `shasum` (or
`sha256sum`). You don't need Go. Here is what `install.sh` does:

1. It downloads the tarball for your machine: macOS or Linux, on arm64 or amd64.
2. It checks the tarball against the release's `SHA256SUMS`.
3. It unpacks the tarball into `~/.local/share/tandem/<version>`. Set `$XDG_DATA_HOME` or
   `TANDEM_SHARE_DIR` to put it somewhere else.
4. It points `~/.local/share/tandem/current` at that version, and links the `tandem` binary
   and the twelve commands from `~/.local/bin` into it.

The installer replaces nothing until the download passes its checksum and the new binary
runs. If an upgrade fails, the old version keeps working.

Run the same line again to upgrade. The installer keeps only the version it just installed.
To install a set release, or a pre-release, name it:

```bash
curl -fsSL https://raw.githubusercontent.com/emsnoel/tandem/main/install.sh | bash -s -- --version v0.1.0
```

`install.sh` takes an optional destination (`bash -s -- ~/bin`), and uses `~/.local/bin`
by default. It warns if that folder is not on `PATH`, because the Herdr keybindings call
the commands by bare name.

**The installer never overwrites an existing Herdr config.** If `~/.config/herdr/config.toml`
doesn't exist, it writes the example config there. If it does exist, the installer leaves
it alone and lists which tandem settings it is missing, so you can copy across only the
parts you want. The commands work without any of those settings. They only add
keybindings, notifications and the role rows in the sidebar. Set `HERDR_CONFIG_PATH` if you
keep the config somewhere else.

Then check your setup from the repo you want to pair in:

```bash
tandem doctor
```

It lists anything that would stop a pairing, such as a missing Herdr integration or agent
CLI, with the command that fixes it.

## Building from source

To work on tandem itself, clone it and install from the checkout. This needs Go 1.26 or
later.

```bash
git clone https://github.com/emsnoel/tandem.git
cd tandem
./install.sh --from-source
```

`--from-source` links the twelve commands to the checkout, so an edit under `bin/` takes
effect at once. **The links store absolute paths. If you move or rename the clone, run
`./install.sh --from-source` again from its new location.** Plain `./install.sh` in a
checkout installs a release instead, and says so.

**The installer compiles the `tandem` binary and copies it, so the copy can go stale.** If
you edit the Go sources, run `./install.sh --from-source` again. The build stamps a hash of
its inputs into the binary: `cmd/`, `internal/`, `go.mod`, `go.sum`, and the embedded SQL
and templates. `tests/run.sh` checks that the hash matches the tree, so a stale binary fails
the check command. `tandem-unpair` runs that check before any teardown, so it tells you
about a stale binary instead of running old gates. `tandem version` prints `dev` for a
source build and the tag for a release.

## Uninstalling

```bash
curl -fsSL https://raw.githubusercontent.com/emsnoel/tandem/main/install.sh | bash -s -- --uninstall
```

From a checkout, run `./install.sh --uninstall`. If you installed somewhere other than
`~/.local/bin`, pass that folder too. The uninstaller removes the command links and the
release folder, or the source build, and nothing else in that folder.

It keeps these, and names each one:

- the Herdr config;
- `~/.config/herdr/tandem.json`;
- any `.bak` copies the install made.

tandem adds trust entries to agent CLIs for each repo. To remove them, run
`tandem-unpair --clean-trust` in each paired repo before you uninstall.

[Versioning](versioning.md) explains the version numbers, and [Releasing](releasing.md)
explains how a release is made.

## Upgrading: a reviewer is now required

The builder no longer reviews anything, on either build engine. Four things change:

- Every repository needs a reviewer before its next review. Add a `reviewer` block to
  `.tandem.json` (see [Per-project configuration](configuration.md#per-project-configuration)),
  or pass `tandem-pair --reviewer KIND` every session. tandem's own `.tandem.json` declares a
  Codex reviewer.
- `tandem-review --builder` and `tandem-verdict --role builder` are gone. Both refuse and
  say why.
- Reviews written before the upgrade don't clear plans. The builder wrote them, so the gate
  refuses with `builder_review`, and `tandem-handoff` asks the reviewer for a new review.
- A legacy or durable build without a reviewer pane no longer starts. A durable run started
  before the upgrade pauses with `no_reviewer`. To move on, stop it (`tandem build stop`),
  end its open jobs (`tandem state recover --run ID`), archive it
  (`tandem state archive --run ID`), pair again with a reviewer, and hand off again.

## Requirements

You need these on every machine:

- Herdr 0.9.3 or later. Every wait reads Herdr's event stream, whose versioned schema
  (protocol 22) arrived in 0.9.3. F27 in `guide/design.md` pins the shapes.
- `jq`, `git`, `bash`, `curl`, `tar`, and `shasum` or `sha256sum`.
- Herdr's integration for each agent you use. `herdr integration install claude` is always
  needed, because the planner is Claude Code. Add `codex`, `copilot` or `opencode` for the
  builder and reviewer kinds you pick. `herdr integration install` lists every kind Herdr
  supports.

You only need the agent CLIs for the kinds you use. tandem never needs all four.

You only need Go 1.26 or later to build from source (`./install.sh --from-source`) and to
run `tests/run.sh`. Releases are built with `CGO_ENABLED=0` for macOS and Linux on arm64
and amd64. That works because the durable state store uses `modernc.org/sqlite`, a SQLite
driver written in Go.

The binary has no bash fallback, on purpose. Two copies of a teardown gate are worse than
one, because a second copy that drifts fails by passing when it should refuse. tandem
still needs `jq`.

tandem is developed and tested on macOS only. `tandem-handoff` checks for BSD or GNU
`stat`, so plan selection should work on Linux, but nobody has run tandem there yet.

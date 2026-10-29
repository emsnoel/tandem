# Install

```bash
curl -fsSL https://raw.githubusercontent.com/emsnoel/tandem/main/install.sh | bash
```

This installs the latest release. You need `curl`, `tar`, `jq` and `shasum` (or
`sha256sum`), and no Go. `install.sh` downloads the tarball for your machine (macOS or
Linux, arm64 or amd64), checks it against the release's `SHA256SUMS`, and unpacks it into
`~/.local/share/tandem/<version>` (`$XDG_DATA_HOME` and `TANDEM_SHARE_DIR` move it).
`~/.local/share/tandem/current` points at that version, and the `tandem` binary and the
twelve commands in `~/.local/bin` are symlinks into it. Nothing is replaced until the
download has passed its checksum and the binary has run, so a failed upgrade leaves the
old version working.

Re-run the same line to upgrade. Only the version just installed is kept. To install a
particular release, or a pre-release, name it:

```bash
curl -fsSL https://raw.githubusercontent.com/emsnoel/tandem/main/install.sh | bash -s -- --version v0.1.0
```

`install.sh` takes an optional destination (`bash -s -- ~/bin`) and defaults to
`~/.local/bin`. It warns if that directory is not on `PATH`, because the Herdr keybindings
resolve these by bare name.

**It will not overwrite an existing Herdr config.** If `~/.config/herdr/config.toml` does
not exist it writes the example there. If it does exist, it leaves it completely alone and
instead lists which of the settings this harness uses are missing, so you can copy across
only the parts you want. Your theme, prefix key and layout are yours; none of the harness
settings are required for the commands to work, they only add keybindings, notifications
and the role rows in the sidebar. (`HERDR_CONFIG_PATH` is honoured if you keep the config
elsewhere.)

## Building from source

To work on tandem itself, clone it and install from the checkout. This needs Go 1.26+.

```bash
git clone https://github.com/emsnoel/tandem.git
cd tandem
./install.sh --from-source
```

`--from-source` symlinks the twelve commands into the checkout, so an edit under `bin/` is
live at once. **The symlinks store absolute paths. If you move or rename the clone,
re-run `./install.sh --from-source` from its new location.** Plain `./install.sh` in a
checkout installs a release instead, and says so.

**The `tandem` binary is compiled and copied, so it can go stale.** If you edit the Go
sources you must re-run `./install.sh --from-source`. The build stamps a hash of its
inputs (`cmd/`, `internal/`, `go.mod`, `go.sum`, embedded SQL and templates) into the
binary, and `tests/run.sh` asserts it matches the tree, so a stale binary fails the check
command, which `tandem-unpair` runs before any teardown. You will be told, rather than
quietly running yesterday's gates. `tandem version` prints `dev` for a source build and
the tag for a release.

## Uninstalling

```bash
curl -fsSL https://raw.githubusercontent.com/emsnoel/tandem/main/install.sh | bash -s -- --uninstall
```

From a checkout, run `./install.sh --uninstall`. Pass the destination too if you installed
somewhere other than `~/.local/bin`. It removes the command links and the release
directory, or the source build, and nothing else in that directory. It keeps the Herdr
config, `~/.config/herdr/tandem.json` and any `.bak` copies the install made, and names
each one. The trust entries tandem added to agent CLIs are per repo. Run
`tandem-unpair --clean-trust` in each paired repo first if you want them gone.

Versions follow [guide/versioning.md](versioning.md), and
[guide/releasing.md](releasing.md) says how a release is cut.

## Upgrading: a reviewer is now required

The builder no longer reviews anything, on either build engine. Four things change:

- Every repository needs a reviewer before its next review. Add a `reviewer` block to
  `.tandem.json` (see [Per-project configuration](configuration.md#per-project-configuration)), or pass
  `tandem-pair --reviewer KIND` every session. Tandem's own `.tandem.json` declares a Codex
  reviewer.
- `tandem-review --builder` and `tandem-verdict --role builder` are gone. Both refuse and
  say why.
- Reviews written before the upgrade do not clear plans. The builder wrote them, so the gate
  refuses with `builder_review` and `tandem-handoff` asks the reviewer for a new review.
- A legacy or durable build without a reviewer pane no longer starts. A durable run started
  before the upgrade pauses with `no_reviewer`. Stop it (`tandem build stop`), end its open
  jobs (`tandem state recover --run ID`), archive it (`tandem state archive --run ID`),
  re-pair with a reviewer, and hand off again.

## Requirements

Herdr 0.9.3+ (every wait reads the event stream whose versioned schema 0.9.3 introduced,
protocol 22; F27 in `guide/design.md` pins the shapes), `jq`, `git`, `bash`, `curl`, `tar`,
`shasum` or `sha256sum`, and the Herdr integration for each agent you use:
`herdr integration install claude` (always required, since the planner is Claude Code), plus
`codex`, `copilot` and/or `opencode` for whichever builder you pick.
`herdr integration install` lists every kind Herdr supports.

The agent CLIs are runtime dependencies only for the kinds you actually use; nothing here
requires all four.

Go 1.26+ is needed only to build from source (`./install.sh --from-source`) and to run
`tests/run.sh`. A release is built with `CGO_ENABLED=0` for macOS and Linux on arm64 and
amd64, which works because the durable state store uses the pure-Go `modernc.org/sqlite`
driver. There is deliberately no bash fallback for the binary: two implementations of a
teardown gate is worse than one, because a diverging second one fails by passing when it
should refuse. `jq` is still required; this work traded silent failure for loud failure in
the places that matter, it did not remove the dependency.

Developed and exercised on macOS only. `tandem-handoff` probes for BSD vs GNU `stat`, so
plan selection should work on Linux, but nothing here has been run there.

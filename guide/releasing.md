# Releasing

Releases are cut by `.github/workflows/release.yml` with release-please. The version
rules are in [versioning.md](versioning.md).

## One-time repo settings

- The repo is public. `install.sh` downloads release files with plain `curl`, which cannot
  read a private repo's releases.
- In Settings, Actions, General, Workflow permissions, "Allow GitHub Actions to create and
  approve pull requests" is on. release-please opens its release PR with the workflow's
  token.

## How a release happens

1. Merge work to `main` with conventional commit subjects (`feat:`, `fix:`). Each push to
   `main` runs `./tests/run.sh`, and a red check stops the job before release-please runs.
2. release-please opens or updates a PR titled `chore(main): release X.Y.Z`. It edits
   `CHANGELOG.md` and `.release-please-manifest.json`.
3. Review that PR. Check that the version is the bump you expect and that the changelog
   lists what changed. A commit with a mistyped type is missing from it.
4. Merge it. release-please creates the tag `vX.Y.Z` and the GitHub Release. The same job
   then runs `tools/package-release.sh` and uploads four tarballs
   (`tandem-{darwin,linux}-{arm64,amd64}.tar.gz`) and `SHA256SUMS` to that release.

## Dry run

Every pull request to `main` runs the check and packages version `v0.0.0`. It publishes
nothing, and the tarballs are kept as the run's `tandem-dist` artifact. To dry-run a branch
without a PR:

```bash
gh workflow run release.yml --ref <branch>
gh run watch
```

`gh workflow run` only works once `release.yml` is on `main`.

## Pre-release

```bash
gh workflow run release.yml --ref main -f prerelease_tag=v0.2.0-rc.1
```

The tag must have a suffix. The release is marked as a pre-release, so `install.sh`
without `--version` keeps installing the latest full release. Testers run
`install.sh --version v0.2.0-rc.1`.

## The minute a new release has no assets

release-please publishes the release before the tarballs are built, and GitHub makes it
`latest` at once. For the minute or two until the upload finishes, `install.sh` without
`--version` fails with a download error naming the URL. Running it again after the upload
works.

## When packaging fails after the release exists

If packaging or upload fails, the release stays without assets, and `install.sh` keeps
failing as above. Re-running the failed run does not help: release-please reports a release
only once, so the re-run packages nothing.

1. Re-package the release from its tag and replace its assets:

   ```bash
   gh workflow run release.yml --ref main -f release_tag=vX.Y.Z
   gh run watch
   ```

   The run checks out the tag, runs the check on it, packages it, and uploads with
   `--clobber`, so a partial earlier upload is replaced.
2. If the tagged code itself needs a fix, ship the fix as the next release. Deleting a
   published release and its tag is not recommended, because anyone who installed it
   already has that version.

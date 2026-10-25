#!/usr/bin/env bash
# install.sh — install tandem into ~/.local/bin.
#
#   install.sh [--version vX.Y.Z] [DEST]     a prebuilt release (the default)
#   install.sh --from-source [DEST]          build this checkout; needs Go
#
# A release is downloaded from GitHub, checked against its SHA256SUMS, unpacked
# into a versioned share directory, and linked into DEST. It needs curl, not Go.
# It also works piped: curl -fsSL .../install.sh | bash
#
# --from-source is for working on tandem itself. The commands are symlinks into
# this checkout, so an edit here is live everywhere, and the binary is built
# with a stamp of its source so tests/run.sh can tell when it has gone stale.
set -euo pipefail

# Everything runs from main, called on the last line, so a download cut off
# mid-pipe runs nothing.
main() {

usage() {
  printf 'usage: install.sh [--version vX.Y.Z] [DEST]\n' >&2
  printf '       install.sh --from-source [DEST]\n' >&2
  exit 2
}
die() { printf 'install: FATAL %s\n' "$*" >&2; exit 1; }
is_tag() { printf '%s\n' "$1" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'; }

# Empty when piped into bash: there is no checkout to build from or link to.
SELF="${BASH_SOURCE[0]:-}"
SRC=""
[ -n "$SELF" ] && [ -f "$SELF" ] && SRC="$(cd "$(dirname "$SELF")" && pwd)"

MODE=release; WANT=""; DEST=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --from-source) MODE=source ;;
    --version)     [ "$#" -ge 2 ] || usage; WANT=$2; shift ;;
    -*)            usage ;;
    *)             [ -z "$DEST" ] || usage; DEST=$1 ;;
  esac
  shift
done
DEST="${DEST:-$HOME/.local/bin}"
if [ -n "$WANT" ]; then
  [ "$MODE" = release ] || usage
  is_tag "$WANT" || usage
fi

command -v jq >/dev/null 2>&1 || {
  printf 'install: FATAL jq is not on PATH.\n' >&2
  printf 'install: every bin/ command parses Herdr'"'"'s JSON output through jq;\n' >&2
  printf 'install: install it (e.g. via your package manager) and re-run.\n' >&2
  exit 1
}

# --- a prebuilt release -------------------------------------------------------
# Nothing in DEST or the share directory changes until the download has passed
# its checksum and the binary has run, so a failed upgrade keeps the old one.
install_release() {
  local os arch asset base url sums sha line v
  case "$(uname -s)" in
    Darwin) os=darwin ;;
    Linux)  os=linux ;;
    *)      os="" ;;
  esac
  case "$(uname -m)" in
    arm64|aarch64) arch=arm64 ;;
    x86_64|amd64)  arch=amd64 ;;
    *)             arch="" ;;
  esac
  [ -n "$os" ] && [ -n "$arch" ] \
    || die "there is no prebuilt release for $(uname -s)/$(uname -m). Build from a checkout with ./install.sh --from-source."
  command -v curl >/dev/null 2>&1 || die "curl is not on PATH; it downloads the release."
  command -v tar  >/dev/null 2>&1 || die "tar is not on PATH; it unpacks the release."
  if command -v shasum >/dev/null 2>&1; then sha="shasum -a 256"
  elif command -v sha256sum >/dev/null 2>&1; then sha="sha256sum"
  else die "neither shasum nor sha256sum is on PATH; one checks the download."
  fi

  asset="tandem-$os-$arch.tar.gz"
  base="${TANDEM_RELEASE_URL:-https://github.com/emsnoel/tandem/releases}"
  if [ -n "$WANT" ]; then base="$base/download/$WANT"; else base="$base/latest/download"; fi
  url="$base/$asset"; sums="$base/SHA256SUMS"

  WORK=$(mktemp -d); trap 'rm -rf "$WORK"' EXIT
  curl -fsSL "$url" -o "$WORK/$asset" \
    || die "could not download $url. Build from a checkout instead with ./install.sh --from-source."
  curl -fsSL "$sums" -o "$WORK/SHA256SUMS" \
    || die "could not download $sums. Build from a checkout instead with ./install.sh --from-source."
  line=$(grep "  $asset\$" "$WORK/SHA256SUMS") || die "SHA256SUMS has no checksum for $asset."
  # shellcheck disable=SC2086
  ( cd "$WORK" && printf '%s\n' "$line" | $sha -c - >/dev/null 2>&1 ) \
    || die "$asset does not match its checksum in SHA256SUMS; the download is corrupt or was changed. Nothing was installed."

  mkdir -p "$WORK/unpack"
  tar -xzf "$WORK/$asset" -C "$WORK/unpack" || die "could not unpack $asset."
  "$WORK/unpack/tandem/tandem" version >/dev/null 2>&1 \
    || die "the downloaded tandem does not run on this machine ($os/$arch)."
  v=$(cat "$WORK/unpack/tandem/VERSION")
  # v names a directory under the share dir that is replaced, so anything but a
  # tag (an empty file, "..") could point the removal outside it.
  is_tag "$v" || die "the release's VERSION file reads '$v', which is not a tag. Nothing was installed."
  [ -z "$WANT" ] || [ "$v" = "$WANT" ] || die "asked for $WANT but the release says it is $v."

  SHARE="${TANDEM_SHARE_DIR:-${XDG_DATA_HOME:-$HOME/.local/share}/tandem}"
  mkdir -p "$SHARE"
  rm -rf "${SHARE:?}/$v"
  mv "$WORK/unpack/tandem" "$SHARE/$v"
  ln -sfn "$v" "$SHARE/current"
  # Only the release just installed is kept; --version reinstalls an older one.
  # Only release directories go: TANDEM_SHARE_DIR may hold other folders.
  for d in "$SHARE"/v*; do
    name=$(basename "$d")
    if [ -d "$d" ] && [ ! -L "$d" ] && [ "$name" != "$v" ] && is_tag "$name"; then
      rm -rf "$d"
    fi
  done

  PAYLOAD="$SHARE/current"
  mkdir -p "$DEST"
  # The binary was always the installer's, so it is replaced without a backup.
  ln -sfn "$PAYLOAD/tandem" "$DEST/tandem"
  printf 'install: installed tandem %s (release) in %s/%s\n' "$v" "$SHARE" "$v"
  if [ -n "$SRC" ] && [ -e "$SRC/.git" ]; then
    printf 'install: running from a checkout; ./install.sh --from-source links this tree instead.\n'
  fi
}

# --- build from this checkout ---
# Go is required. A bash fallback would duplicate the teardown gates.
install_source() {
  [ -n "$SRC" ] || die "--from-source needs a checkout: clone https://github.com/emsnoel/tandem and run ./install.sh --from-source there."
  command -v go >/dev/null 2>&1 || {
    printf 'install: FATAL go is not on PATH.\n' >&2
    printf 'install: --from-source compiles tandem from this repo; install Go and re-run,\n' >&2
    printf 'install: or drop --from-source to install a prebuilt release.\n' >&2
    printf 'install:   https://go.dev/dl/\n' >&2
    exit 1
  }
  # The source fingerprint, shared with the staleness test.
  local hash
  hash=$("$SRC/bin/lib/source-hash.sh" "$SRC") || die "could not hash the build inputs."
  [ -n "$hash" ] || die "the build inputs hashed to nothing."

  mkdir -p "$DEST"
  # After a release install, DEST/tandem is a symlink into the share directory.
  # Remove it so the build never writes through it.
  rm -f "$DEST/tandem"
  ( cd "$SRC" && go build -ldflags "-X main.sourceHash=$hash" -o "$DEST/tandem" ./cmd/tandem ) \
    || die "go build failed."
  PAYLOAD="$SRC"
  printf 'install: built %s/tandem (source %s)\n' "$DEST" "${hash:0:12}"
}

PAYLOAD=""
if [ "$MODE" = source ]; then install_source; else install_release; fi

for f in "$PAYLOAD"/bin/*; do
  # Commands only. bin/lib holds helpers the commands call, not commands.
  [[ -f "$f" ]] || continue
  name=$(basename "$f")
  target="$DEST/$name"
  if [[ -e "$target" && ! -L "$target" ]]; then
    mv "$target" "$target.bak.$(date +%Y%m%d%H%M%S)"
    printf 'install: backed up existing %s\n' "$name"
  fi
  ln -sfn "$f" "$target"
  printf 'install: %s -> %s\n' "$target" "$f"
done

printf '\n'
case ":$PATH:" in
  *":$DEST:"*) printf 'install: %s is on PATH.\n' "$DEST" ;;
  *)           printf 'install: WARNING %s is NOT on PATH; the keybindings will not find these.\n' "$DEST" ;;
esac

# --- Herdr config ---
# Never overwrite an existing config.
CONFIG="${HERDR_CONFIG_PATH:-$HOME/.config/herdr/config.toml}"
EXAMPLE="$PAYLOAD/config/config.toml.example"

printf '\n'
if [[ ! -e "$CONFIG" ]]; then
  mkdir -p "$(dirname "$CONFIG")"
  cp "$EXAMPLE" "$CONFIG"
  printf 'install: wrote %s\n' "$CONFIG"
else
  printf 'install: %s already exists — left untouched.\n' "$CONFIG"
  missing=()
  while IFS='|' read -r pattern label; do
    grep -qE "$pattern" "$CONFIG" || missing+=("$label")
  done <<'KEYS'
^[[:space:]]*resume_agents_on_restore[[:space:]]*=|[session] resume_agents_on_restore
^[[:space:]]*agent_panel_sort[[:space:]]*=|[ui] agent_panel_sort
^[[:space:]]*status_indicators[[:space:]]*=|[ui] status_indicators
^[[:space:]]*show_agent_labels_on_pane_borders[[:space:]]*=|[ui] show_agent_labels_on_pane_borders
^[[:space:]]*delivery[[:space:]]*=|[ui.toast] delivery
rows_by_agent|[ui.sidebar.agents.rows_by_agent] role rows
^[[:space:]]*next_agent[[:space:]]*=|[keys] next_agent
command[[:space:]]*=[[:space:]]*"tandem-pair"|[[keys.command]] tandem-pair binding
command[[:space:]]*=[[:space:]]*"tandem-handoff"|[[keys.command]] tandem-handoff binding
KEYS

  if (( ${#missing[@]} == 0 )); then
    printf 'install: it already has every setting this harness uses.\n'
  else
    printf 'install: it is missing %s of the settings this harness uses:\n' "${#missing[@]}"
    printf '  - %s\n' "${missing[@]}"
    printf 'install: these are quality-of-life only — the commands work without them.\n'
    printf 'install: copy what you want from %s\n' "$EXAMPLE"
  fi
fi

if command -v herdr >/dev/null 2>&1; then
  printf '\n'
  printf 'install: herdr config check -> '
  herdr config check || true
fi

printf '\ninstall: next, run tandem-init in each repo you want to pair in.\n'
}

main "$@"

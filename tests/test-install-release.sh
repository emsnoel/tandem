#!/usr/bin/env bash
# install.sh installs a prebuilt release by default, so users never need Go.
# Releases come from file:// fixtures built by tools/package-release.sh, and a
# `go` stub records any call.
#
# Isolated: DEST, the share directory and the Herdr config are in one temp dir.
set -uo pipefail
ROOT="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
PASS=0; FAIL=0
ok()   { printf '  ok   %s\n' "$1"; PASS=$((PASS+1)); }
nope() { printf '  FAIL %s\n' "$1"; FAIL=$((FAIL+1)); }
done_() { printf '\ninstall-release: passed %d, failed %d\n' "$PASS" "$FAIL"; [ "$FAIL" -eq 0 ]; exit $?; }

TMP=$(mktemp -d); trap 'rm -rf "$TMP"' EXIT
REL="$TMP/rel"; DEST="$TMP/dest"; SHARE="$TMP/share"
ASSET="tandem-$(go env GOOS)-$(go env GOARCH).tar.gz"
HOST="$(go env GOOS)/$(go env GOARCH)"

# --- fixtures, built with the real Go before the stub goes on PATH ------------
for v in v9.9.8 v9.9.9 v9.9.9-rc.1; do
  "$ROOT/tools/package-release.sh" "$v" "$REL/download/$v" "$HOST" >/dev/null 2>&1 \
    || { nope "could not package the $v fixture"; done_; }
done
mkdir -p "$REL/latest" && cp -R "$REL/download/v9.9.9" "$REL/latest/download"

STUB="$TMP/stub"; mkdir -p "$STUB"
cat > "$STUB/go" <<EOF
#!/usr/bin/env bash
echo "go \$*" >> "$TMP/go-called"
exit 1
EOF
chmod +x "$STUB/go"

# inst [ENV=VALUE ...] -- ARGS: run install.sh with the go stub first on PATH.
inst() {
  local envs=()
  while [ "$1" != "--" ]; do envs+=("$1"); shift; done; shift
  env TANDEM_RELEASE_URL="file://$REL" TANDEM_SHARE_DIR="$SHARE" \
      HERDR_CONFIG_PATH="$TMP/herdr/config.toml" PATH="$STUB:$PATH" \
      ${envs[@]+"${envs[@]}"} "$ROOT/install.sh" "$@" 2>&1
}
installed() { "$DEST/tandem" version 2>/dev/null | awk '{print $2}'; }

# --- install the latest release ----------------------------------------------
OUT=$(inst -- "$DEST"); RC=$?
[ "$RC" -eq 0 ] && ok "installs the latest release" || { nope "install failed (rc $RC): $OUT"; done_; }
[ "$(installed)" = v9.9.9 ] && ok "and it is v9.9.9" || nope "installed '$(installed)'"
[ -e "$TMP/go-called" ] && nope "and calls go: $(cat "$TMP/go-called")" || ok "without calling go"
case "$(readlink "$DEST/tandem-ask")" in
  "$SHARE/current/bin/tandem-ask") ok "commands link into the share directory's current release" ;;
  *) nope "tandem-ask links to '$(readlink "$DEST/tandem-ask")'" ;;
esac
[ -f "$SHARE/current/bin/lib/reviewer.sh" ] && ok "the commands' helpers sit beside them" || nope "bin/lib is missing from the release"
[ -f "$TMP/herdr/config.toml" ] && ok "writes the example Herdr config when none exists" || nope "no Herdr config written"
case "$OUT" in *"--from-source links this tree"*) ok "run from a checkout, it names --from-source" ;;
  *) nope "no --from-source hint from a checkout: $OUT" ;; esac

# --- a pinned version, then an upgrade ---------------------------------------
OUT=$(inst -- --version v9.9.8 "$DEST"); RC=$?
[ "$RC" -eq 0 ] && [ "$(installed)" = v9.9.8 ] && ok "--version installs that release" || nope "--version v9.9.8 gave '$(installed)' (rc $RC): $OUT"
inst -- "$DEST" >/dev/null; LEFT=$(ls "$SHARE" | sort | tr '\n' ' ')
[ "$(installed)" = v9.9.9 ] && ok "re-running upgrades to the latest" || nope "upgrade left '$(installed)'"
[ "$LEFT" = "current v9.9.9 " ] && ok "and removes the old release" || nope "the share directory holds: $LEFT"

OUT=$(inst -- --version v9.9.9-rc.1 "$DEST"); RC=$?
[ "$RC" -eq 0 ] && [ "$(installed)" = v9.9.9-rc.1 ] && ok "--version installs a pre-release" || nope "pre-release gave '$(installed)' (rc $RC): $OUT"
inst -- "$DEST" >/dev/null

# --- a corrupt download changes nothing --------------------------------------
BAD="$TMP/bad"; mkdir -p "$BAD/latest" && cp -R "$REL/latest/download" "$BAD/latest/"
printf 'x' >> "$BAD/latest/download/$ASSET"
BEFORE=$(readlink "$SHARE/current")
OUT=$(inst TANDEM_RELEASE_URL="file://$BAD" -- "$DEST"); RC=$?
[ "$RC" -eq 1 ] && ok "refuses a tarball whose checksum does not match" || nope "a corrupt tarball exited $RC"
case "$OUT" in *checksum*) ok "and says checksum" ;; *) nope "and says checksum (got: $OUT)" ;; esac
[ "$(readlink "$SHARE/current")" = "$BEFORE" ] && [ "$(installed)" = v9.9.9 ] \
  && ok "and leaves the installed release alone" || nope "a failed install changed the installed release"

# --- a release with no asset for this machine ---------------------------------
OUT=$(inst TANDEM_RELEASE_URL="file://$TMP/empty" -- "$DEST"); RC=$?
[ "$RC" -eq 1 ] && ok "a missing download fails" || nope "a missing download exited $RC"
case "$OUT" in *"file://$TMP/empty"*"--from-source"*|*"--from-source"*"file://$TMP/empty"*) ok "naming the URL and --from-source" ;;
  *) nope "naming the URL and --from-source (got: $OUT)" ;; esac

# --- platform names ---------------------------------------------------------
UN="$TMP/uname"; mkdir -p "$UN"
fake_uname() { printf '#!/usr/bin/env bash\ncase "$1" in -s) echo %s ;; -m) echo %s ;; *) echo %s ;; esac\n' "$1" "$2" "$1" > "$UN/uname"; chmod +x "$UN/uname"; }
fake_uname Linux aarch64
OUT=$(inst PATH="$UN:$STUB:$PATH" -- "$DEST"); RC=$?
case "$OUT" in *tandem-linux-arm64.tar.gz*) ok "Linux aarch64 asks for the linux-arm64 asset" ;;
  *) nope "Linux aarch64 asked for something else (rc $RC): $OUT" ;; esac
fake_uname FreeBSD amd64
OUT=$(inst PATH="$UN:$STUB:$PATH" -- "$DEST"); RC=$?
case "$RC:$OUT" in 1:*FreeBSD*--from-source*) ok "an unsupported OS refuses and names --from-source" ;;
  *) nope "FreeBSD gave rc $RC: $OUT" ;; esac

# --- piped from curl --------------------------------------------------------
OUT=$(env TANDEM_RELEASE_URL="file://$REL" TANDEM_SHARE_DIR="$SHARE" HERDR_CONFIG_PATH="$TMP/herdr/config.toml" \
      PATH="$STUB:$PATH" bash -s -- "$DEST" < "$ROOT/install.sh" 2>&1); RC=$?
[ "$RC" -eq 0 ] && [ "$(installed)" = v9.9.9 ] && ok "works piped into bash" || nope "piped install failed (rc $RC): $OUT"
OUT=$(env PATH="$STUB:$PATH" bash -s -- --from-source "$DEST" < "$ROOT/install.sh" 2>&1); RC=$?
case "$RC:$OUT" in 1:*"needs a checkout"*) ok "piped --from-source refuses: it needs a checkout" ;;
  *) nope "piped --from-source gave rc $RC: $OUT" ;; esac

# --- switching between a source build and a release -------------------------
OUT=$(env HERDR_CONFIG_PATH="$TMP/herdr/config.toml" TANDEM_SHARE_DIR="$SHARE" "$ROOT/install.sh" --from-source "$DEST" 2>&1); RC=$?
[ "$RC" -eq 0 ] && ok "--from-source builds over a release install" || nope "--from-source failed (rc $RC): $OUT"
[ -f "$DEST/tandem" ] && [ ! -L "$DEST/tandem" ] && [ "$(installed)" = dev ] \
  && ok "and DEST/tandem is the dev build" || nope "DEST/tandem after --from-source: '$(installed)'"
[ "$("$SHARE/current/tandem" version | awk '{print $2}')" = v9.9.9 ] \
  && ok "and the release copy was not written through" || nope "the build overwrote the release copy"
[ "$(readlink "$DEST/tandem-ask")" = "$ROOT/bin/tandem-ask" ] && ok "and the commands link into the checkout" \
  || nope "tandem-ask links to '$(readlink "$DEST/tandem-ask")'"
inst -- "$DEST" >/dev/null
[ -L "$DEST/tandem" ] && [ "$(installed)" = v9.9.9 ] && ok "a release install replaces the dev build" || nope "the release did not replace the dev build"
ls "$DEST" | grep -q '\.bak\.' && nope "and leaves a .bak behind: $(ls "$DEST")" || ok "without a .bak"

# --- piped, with a child that reads stdin ---
# Under curl | bash the script is stdin, so it must be parsed before anything
# runs.
HS="$TMP/herdr-stub"; mkdir -p "$HS"
printf '#!/usr/bin/env bash\ncat >/dev/null\necho checked\n' > "$HS/herdr"; chmod +x "$HS/herdr"
OUT=$(env TANDEM_RELEASE_URL="file://$REL" TANDEM_SHARE_DIR="$SHARE" HERDR_CONFIG_PATH="$TMP/herdr/config.toml" \
      PATH="$HS:$STUB:$PATH" bash -s -- "$DEST" < "$ROOT/install.sh" 2>&1)
case "$OUT" in *"next, run tandem-init"*) ok "piped, a child reading stdin does not cut the script short" ;;
  *) nope "piped, the script stopped early: $OUT" ;; esac

# --- the share directory is only ever pruned of releases ---------------------
SHARED="$TMP/shared"; mkdir -p "$SHARED/other-app"
inst TANDEM_SHARE_DIR="$SHARED" -- "$DEST" >/dev/null
[ -d "$SHARED/other-app" ] && ok "a TANDEM_SHARE_DIR shared with other folders keeps them" \
  || nope "installing deleted another folder in TANDEM_SHARE_DIR"
inst -- "$DEST" >/dev/null

# --- a VERSION file that is not a tag ---
# It names a directory under the share dir, so ".." would delete its parent.
for bad in .. ""; do
  BV="$TMP/badver"; rm -rf "$BV" "$TMP/bvx"; mkdir -p "$BV/latest/download" "$TMP/bvx"
  tar -C "$TMP/bvx" -xzf "$REL/latest/download/$ASSET"
  printf '%s\n' "$bad" > "$TMP/bvx/tandem/VERSION"
  COPYFILE_DISABLE=1 tar -C "$TMP/bvx" -czf "$BV/latest/download/$ASSET" tandem
  ( cd "$BV/latest/download" && { shasum -a 256 "$ASSET" 2>/dev/null || sha256sum "$ASSET"; } > SHA256SUMS )
  NEST="$TMP/nest"; rm -rf "$NEST"; mkdir -p "$NEST/share" "$NEST/keep"
  OUT=$(inst TANDEM_RELEASE_URL="file://$BV" TANDEM_SHARE_DIR="$NEST/share" -- "$DEST"); RC=$?
  [ "$RC" -eq 1 ] && ok "refuses a release whose VERSION is '$bad'" || nope "VERSION '$bad' exited $RC: $OUT"
  [ -d "$NEST/keep" ] && [ -d "$NEST/share" ] && ok "and deletes nothing around the share directory" \
    || nope "VERSION '$bad' deleted folders outside the release"
done

# --- bad arguments ----------------------------------------------------------
for args in "--version 1.0" "--bogus" "--from-source --version v1.0.0"; do
  # shellcheck disable=SC2086
  inst -- $args "$DEST" >/dev/null; RC=$?
  [ "$RC" -eq 2 ] && ok "refuses '$args' as usage" || nope "'$args' exited $RC, want 2"
done

done_

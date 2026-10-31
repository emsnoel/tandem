# bin/lib/relay.sh: keep the relay mailbox out of version control.
#
# Sourced by bin/tandem-pair, never run. Callers define info and die.
# bin/tandem-init keeps its own copy, with its own messages, until roadmap
# item 5 moves it.

# shellcheck shell=bash

# ensure_relay_ignored <repo-root>: keep the mailbox out of version control.
# tandem-pair creates the mailbox, so it ignores it even when tandem-init never
# ran.
ensure_relay_ignored() {
  local root="$1" gi="$1/.gitignore" base re
  git -C "$root" rev-parse --git-dir >/dev/null 2>&1 || return 0
  # tandem resolves the name, so a repo with the legacy .herdr-relay gets that
  # name ignored.
  base=$(tandem paths relay --root "$root") || die "the installed tandem is behind these commands (no 'tandem paths'); re-run install.sh."
  base=$(basename "$base")
  re="^/?${base//./\\.}/?$"
  if [[ -f "$gi" ]] && grep -qE "$re" "$gi"; then
    return 0
  fi
  # The leading newline ends a final .gitignore line that lacks one.
  if printf '\n# tandem agent relay mailbox (local IPC, never committed)\n%s/\n' "$base" >> "$gi"; then
    info "added $base/ to $gi"
  else
    info "WARNING could not write $gi; $base will show up as untracked."
  fi
}

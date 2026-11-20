# bin/lib/git-exclude.sh: keep tandem's local folders out of version control.
#
# Sourced by bin/tandem-init, bin/tandem-pair and bin/tandem-ask, never run.
#
# tandem writes the entry to git's exclude file, never to .gitignore. The
# .gitignore is usually tracked, and a run stops when a tracked file changes
# after tandem measured the tree. The exclude file lives in the common git
# directory, so one entry covers every worktree of the repo.

# shellcheck shell=bash

# git_exclude <repo-root> <name>: make git ignore <name>/ at the repo root.
# Prints the exclude file when it added the entry. Prints nothing when git
# already ignores the name or <repo-root> is not a git repo. Returns 1 when the
# entry could not be written.
git_exclude() {
  local root="$1" name="$2" file
  git -C "$root" rev-parse --git-dir >/dev/null 2>&1 || return 0
  # check-ignore sees .gitignore, the exclude file and the user's global
  # excludes, so a repo that already ignores the name is left alone.
  git -C "$root" check-ignore -q --no-index "$name/" 2>/dev/null && return 0
  file=$(git -C "$root" rev-parse --git-path info/exclude 2>/dev/null) || return 1
  [[ "$file" == /* ]] || file="$root/$file"
  mkdir -p "${file%/*}" 2>/dev/null || return 1
  # The leading newline ends a final line that lacks one. Grouped so 2>/dev/null
  # also catches a failed redirection.
  { printf '\n# tandem local state, never committed\n/%s/\n' "$name" >> "$file"; } 2>/dev/null || return 1
  printf '%s\n' "$file"
}

# ensure_relay_ignored <repo-root>: keep the mailbox out of version control.
# tandem-pair creates the mailbox, so it ignores it even when tandem-init never
# ran. Callers define info and die.
ensure_relay_ignored() {
  local root="$1" base file
  # tandem resolves the name, so a repo with the legacy .herdr-relay gets that
  # name ignored.
  base=$(tandem paths relay --root "$root") || die "the installed tandem is behind these commands (no 'tandem paths'); re-run install.sh."
  base=$(basename "$base")
  if file=$(git_exclude "$root" "$base"); then
    [[ -z "$file" ]] || info "added $base/ to $file"
  else
    info "WARNING could not write git's exclude file; $base will show up as untracked."
  fi
}

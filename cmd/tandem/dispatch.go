// Dispatch from `tandem <verb>` to the bash entry points.
//
// A verb that is not a compiled subcommand resolves to a `tandem-<verb>`
// executable on PATH, so `tandem pair` runs bin/tandem-pair. The scripts stay
// directly runnable.
//
// The binary is copied while the scripts are symlinked, so it can lag the repo:
//   - Routing is a PATH lookup, so a stale binary still finds new scripts.
//   - Handover is execve, so the script's exit code and signals reach the
//     caller unchanged. internal/exitcode gives exact numbers meaning.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/emsnoel/tandem/internal/exitcode"
)

// delegatePrefix is both how a delegate is named on disk and how it is found:
// the verb is what remains after stripping it.
const delegatePrefix = "tandem-"

// coreSubcommands are the verbs this binary answers itself, for usage output.
// run() is the authority.
var coreSubcommands = []string{"agent", "artifact", "build", "check", "collaboration", "config", "decision", "doctor", "gate", "hooks", "pane", "paths", "relay", "reviewer", "state", "tab", "task", "triage", "trust", "usage", "version"}

// validVerb reports whether verb may be joined to delegatePrefix and looked up
// on PATH. exec.LookPath treats anything with a separator as a path, so only a
// lowercase word is allowed, and only a PATH entry named tandem-<word> can run.
func validVerb(verb string) bool {
	if verb == "" || len(verb) > 32 {
		return false
	}
	for i, r := range verb {
		switch {
		case r >= 'a' && r <= 'z':
			// A letter is allowed anywhere, including first.
		case r >= '0' && r <= '9' || r == '-':
			// A digit or dash is allowed only after a letter, so a verb can
			// never begin with a dash and be mistaken for a flag.
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// resolveDelegate returns the path of the executable implementing verb, or ""
// if there is none, including for a rejected verb.
func resolveDelegate(verb string) string {
	if !validVerb(verb) {
		return ""
	}
	path, err := exec.LookPath(delegatePrefix + verb)
	if err != nil {
		return ""
	}
	return path
}

// delegateVerbs lists the delegates on PATH now, deduplicated and sorted.
// Unreadable PATH entries are skipped.
func delegateVerbs() []string {
	seen := make(map[string]bool)
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			if !strings.HasPrefix(name, delegatePrefix) {
				continue
			}
			verb := strings.TrimPrefix(name, delegatePrefix)
			if !validVerb(verb) || seen[verb] {
				continue
			}
			// Skip directories and files nobody may execute. Stat follows symlinks, which
			// install.sh creates.
			info, err := os.Stat(filepath.Join(dir, name))
			if err != nil || info.IsDir() || info.Mode().Perm()&0o111 == 0 {
				continue
			}
			seen[verb] = true
		}
	}
	verbs := make([]string, 0, len(seen))
	for v := range seen {
		verbs = append(verbs, v)
	}
	sort.Strings(verbs)
	return verbs
}

// usage writes what this binary can do: the verbs it answers itself, then the
// delegates it can reach.
func usage(w *os.File) {
	fmt.Fprintf(w, "usage: tandem <%s> ...\n", strings.Join(coreSubcommands, "|"))
	if verbs := delegateVerbs(); len(verbs) > 0 {
		fmt.Fprintf(w, "       tandem <%s> ...\n", strings.Join(verbs, "|"))
		fmt.Fprintf(w, "         (also runnable directly as %s<verb>)\n", delegatePrefix)
	}
}

// delegate hands this process over to the script implementing verb. It returns
// only on failure: a successful Exec never comes back.
func delegate(verb string, args []string) int {
	path := resolveDelegate(verb)
	if path == "" {
		fmt.Fprintf(os.Stderr, "tandem: unknown subcommand %q, and no %s%s on PATH\n",
			verb, delegatePrefix, verb)
		usage(os.Stderr)
		return exitcode.Usage
	}
	// argv[0] is the delegate's own name, not tandem's, so the script's usage
	// and error text name the command a human would type to reach it.
	argv := append([]string{delegatePrefix + verb}, args...)
	err := syscall.Exec(path, argv, os.Environ())
	// Only reached if the exec itself failed.
	fmt.Fprintf(os.Stderr, "tandem: could not run %s: %v\n", path, err)
	return exitcode.Internal
}

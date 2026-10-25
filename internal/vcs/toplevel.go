package vcs

import (
	"os/exec"
	"strings"
)

// Toplevel returns the physical git repository root containing dir, as "git -C
// dir rev-parse --show-toplevel" does. It errors outside a repository.
func Toplevel(dir string) (string, error) {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

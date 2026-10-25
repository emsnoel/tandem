package vcs

import (
	"errors"
	"os/exec"
	"strings"
)

// TaskDiff is the change to exactly these files, new files included.
//
// Legacy route only: it is relative to HEAD in a shared worktree, so it cannot
// separate this task's edits from other uncommitted work. Durable runs use
// internal/snapshot.
func TaskDiff(root string, files []string) (string, error) {
	var tracked, fresh []string
	for _, f := range files {
		if err := exec.Command("git", "-C", root, "ls-files", "--error-unmatch", "--", f).Run(); err == nil {
			tracked = append(tracked, f)
		} else {
			fresh = append(fresh, f)
		}
	}

	var out strings.Builder
	if len(tracked) > 0 {
		args := append([]string{"-C", root, "diff", "HEAD", "--"}, tracked...)
		b, err := exec.Command("git", args...).Output()
		if err != nil {
			return "", err
		}
		out.Write(b)
	}
	for _, f := range fresh {
		cmd := exec.Command("git", "-C", root, "diff", "--no-index", "--", "/dev/null", f)
		b, err := cmd.Output()
		var exitErr *exec.ExitError
		if err != nil && !(errors.As(err, &exitErr) && exitErr.ExitCode() == 1) {
			continue
		}
		out.Write(b)
	}
	return out.String(), nil
}

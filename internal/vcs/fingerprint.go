// Package vcs reads version-control state for callers that keep policy pure.
package vcs

import "os/exec"

// Fingerprint returns git's porcelain working-tree status, optionally limited
// to pathspecs relative to root. Empty paths means the whole tree.
func Fingerprint(root string, paths []string) (string, error) {
	args := []string{"-C", root, "status", "--porcelain"}
	// "--" keeps a pathspec from being read as a flag or branch name.
	if len(paths) > 0 {
		args = append(args, "--")
		args = append(args, paths...)
	}
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

//go:build darwin || linux

package runner

import (
	"os/exec"
	"syscall"
	"time"
)

// processGroups: a review can be stopped with everything it started.
const processGroups = true

// inOwnGroup starts cmd as the leader of a new process group, so that stopping
// the review can stop every process it started.
func inOwnGroup(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

// killGroup kills every process in the group led by pid. A group that is already
// gone is not an error.
func killGroup(pid int) { _ = syscall.Kill(-pid, syscall.SIGKILL) }

// stopGroup asks the group to stop, waits up to grace for the leader to exit,
// and then kills the group.
func stopGroup(pid int, grace time.Duration, done <-chan error) {
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	select {
	case <-done:
		return
	case <-time.After(grace):
	}
	killGroup(pid)
	<-done
}

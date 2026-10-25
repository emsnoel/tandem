package check

import (
	"os/exec"
	"syscall"
)

// setProcessGroup starts the check in its own process group and makes a
// cancelled context kill that group, not just bash.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

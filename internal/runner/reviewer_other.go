//go:build !darwin && !linux

package runner

import (
	"os/exec"
	"time"
)

const processGroups = false

// Process groups are not available here, so a direct review could not be
// stopped reliably: the entry points refuse before these are reached.
func inOwnGroup(*exec.Cmd)                       {}
func killGroup(int)                              {}
func stopGroup(int, time.Duration, <-chan error) {}

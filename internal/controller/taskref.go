package controller

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// ErrNoSuchTask is a task reference that names no task of the run, or more
// than one.
var ErrNoSuchTask = errors.New("no such task")

var taskNumberRE = regexp.MustCompile(`^(task-)?[1-9][0-9]{0,8}$`)

// ResolveTask maps a task reference to its stable id through the run manifest.
// The reference is the task's number (3), task-N for that number, or the
// task's id as the plan's Task-ID line gives it and status prints it. A Task-ID
// always starts with a letter, so a bare number is never one. task-N can be
// both a number and another task's Task-ID, and is then refused.
func ResolveTask(runDir, ref string) (string, error) {
	m, err := ReadManifest(runDir)
	if err != nil {
		return "", err
	}
	return resolveTaskRef(m, ref)
}

func resolveTaskRef(m RunManifest, ref string) (string, error) {
	ordinal := 0
	if taskNumberRE.MatchString(ref) {
		ordinal, _ = strconv.Atoi(strings.TrimPrefix(ref, "task-"))
	}
	var found []string
	for _, t := range m.Tasks {
		if t.ID == ref || t.Ordinal == ordinal {
			found = append(found, t.ID)
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return "", fmt.Errorf("%w: the run has no task %q; give a task's number or its id: %s", ErrNoSuchTask, ref, taskList(m))
	}
	return "", fmt.Errorf("%w: %q names more than one task (%s); give a task's number or its id: %s",
		ErrNoSuchTask, ref, strings.Join(found, " and "), taskList(m))
}

// taskList names every task of the run as "1 (id), 2 (id)".
func taskList(m RunManifest) string {
	parts := make([]string, len(m.Tasks))
	for i, t := range m.Tasks {
		parts[i] = fmt.Sprintf("%d (%s)", t.Ordinal, t.ID)
	}
	return strings.Join(parts, ", ")
}

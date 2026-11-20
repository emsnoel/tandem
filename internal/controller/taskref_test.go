package controller

import (
	"errors"
	"strings"
	"testing"
)

func TestResolveTaskRef(t *testing.T) {
	m := RunManifest{Tasks: []ManifestTask{{Ordinal: 1, ID: "progress-repo"}, {Ordinal: 2, ID: "task-1"}, {Ordinal: 3, ID: "task-3"}}}
	for ref, want := range map[string]string{
		"1":             "progress-repo",
		"progress-repo": "progress-repo",
		"2":             "task-1",
		"task-2":        "task-1",
		"3":             "task-3",
		"task-3":        "task-3", // its number and its id name the same task
	} {
		if got, err := resolveTaskRef(m, ref); err != nil || got != want {
			t.Errorf("%q -> %q %v, want %q", ref, got, err, want)
		}
	}
	for ref, want := range map[string]string{
		"task-1": `"task-1" names more than one task (progress-repo and task-1)`,
		"4":      `no task "4"`,
		"0":      `no task "0"`,
		"+1":     `no task "+1"`,
		"01":     `no task "01"`,
		"task-":  `no task "task-"`,
		"":       `no task ""`,
	} {
		_, err := resolveTaskRef(m, ref)
		if !errors.Is(err, ErrNoSuchTask) || !strings.Contains(err.Error(), want) ||
			!strings.Contains(err.Error(), "1 (progress-repo), 2 (task-1), 3 (task-3)") {
			t.Errorf("%q: %v, want %q and the task list", ref, err, want)
		}
	}
}

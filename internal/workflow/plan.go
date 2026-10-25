package workflow

import (
	"fmt"
	"path"
	"strings"
)

// ValidTaskID reports whether id is a task slug: lowercase letters, digits and
// hyphens, starting with a letter, at most 64 characters. The state store
// applies the same rule to the ids it persists.
func ValidTaskID(id string) bool {
	if len(id) == 0 || len(id) > 64 || id[0] < 'a' || id[0] > 'z' {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

// ValidatePlan checks a plan's tasks as a whole: every id is a slug and appears
// once, every dependency names another task, no dependency is repeated, paths
// stay inside the checkout, and the dependencies form no cycle. A cycle is
// reported as the path around it, found in plan order so the same plan always
// gives the same message.
//
// It is pure and says nothing about order of execution: the task list's order
// is the plan's, and identity is the id.
func ValidatePlan(tasks []TaskSpec) error {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("%w: plan: %s", ErrInvalidTransition, fmt.Sprintf(format, a...))
	}
	if len(tasks) == 0 {
		return bad("the plan has no tasks")
	}
	byID := map[string]TaskSpec{}
	for _, t := range tasks {
		if !ValidTaskID(t.ID) {
			return bad("task id %q is not valid: lowercase letters, digits and hyphens, starting with a letter, at most 64 characters", t.ID)
		}
		if _, dup := byID[t.ID]; dup {
			return bad("task id %q is declared twice", t.ID)
		}
		byID[t.ID] = t
	}
	for _, t := range tasks {
		seen := map[string]bool{}
		for _, d := range t.DependsOn {
			switch {
			case d == t.ID:
				return bad("task %s depends on itself", t.ID)
			case seen[d]:
				return bad("task %s lists dependency %q twice", t.ID, d)
			}
			if _, ok := byID[d]; !ok {
				return bad("task %s depends on %q, which is not a task of the plan", t.ID, d)
			}
			seen[d] = true
		}
		for _, p := range t.Paths {
			if err := ValidRelPath(p); err != nil {
				return bad("task %s: path %q %v", t.ID, p, err)
			}
		}
		for _, o := range t.Outputs {
			if strings.TrimSpace(o) == "" {
				return bad("task %s has an empty output", t.ID)
			}
		}
	}
	if cycle := findCycle(tasks, byID); cycle != nil {
		return bad("the task dependencies form a cycle: %s", strings.Join(cycle, " -> "))
	}
	return nil
}

// ValidRelPath accepts a relative, forward-slash prefix inside the checkout. It
// is the one rule for the paths a plan declares and the scopes memory binds to.
func ValidRelPath(p string) error {
	if strings.TrimSpace(p) == "" {
		return fmt.Errorf("is empty")
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("has a control character")
		}
	}
	if strings.Contains(p, `\`) || path.IsAbs(p) {
		return fmt.Errorf("escapes the checkout or is not a relative path")
	}
	if c := path.Clean(p); c == ".." || strings.HasPrefix(c, "../") {
		return fmt.Errorf("escapes the checkout or is not a relative path")
	}
	return nil
}

// findCycle returns the first dependency cycle in plan order as a closed path
// (a, b, ..., a), or nil.
func findCycle(tasks []TaskSpec, byID map[string]TaskSpec) []string {
	const (
		unseen = iota
		onPath
		done
	)
	color := map[string]int{}
	var stack []string
	var visit func(id string) []string
	visit = func(id string) []string {
		color[id] = onPath
		stack = append(stack, id)
		for _, d := range byID[id].DependsOn {
			switch color[d] {
			case onPath:
				i := len(stack) - 1
				for stack[i] != d {
					i--
				}
				return append(append([]string{}, stack[i:]...), d)
			case unseen:
				if c := visit(d); c != nil {
					return c
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[id] = done
		return nil
	}
	for _, t := range tasks {
		if color[t.ID] == unseen {
			if c := visit(t.ID); c != nil {
				return c
			}
		}
	}
	return nil
}

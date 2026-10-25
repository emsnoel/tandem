package workflow

import (
	"errors"
	"strings"
	"testing"
)

func spec(id string, deps ...string) TaskSpec {
	return TaskSpec{ID: id, Brief: "b", Constraints: "c", DependsOn: deps, Paths: []string{}, Outputs: []string{}}
}

func TestValidatePlanAcceptsAnExplicitDAG(t *testing.T) {
	tasks := []TaskSpec{spec("d", "b", "c"), spec("b", "a"), spec("c", "a"), spec("a")}
	if err := ValidatePlan(tasks); err != nil {
		t.Fatalf("a diamond listed out of order is a valid plan: %v", err)
	}
}

func TestPlanCycleReportsPath(t *testing.T) {
	tasks := []TaskSpec{spec("a", "c"), spec("b", "a"), spec("c", "b")}
	err := ValidatePlan(tasks)
	if err == nil || !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("err %v", err)
	}
	if !strings.Contains(err.Error(), "a -> c -> b -> a") {
		t.Errorf("the error must name the cycle: %v", err)
	}
	// The path does not depend on map order: the same input always says the same.
	for i := 0; i < 50; i++ {
		if again := ValidatePlan(tasks); again.Error() != err.Error() {
			t.Fatalf("run %d: %v != %v", i, again, err)
		}
	}
	// A cycle reachable from an acyclic head names only the cycle.
	tasks = []TaskSpec{spec("root"), spec("x", "root", "y"), spec("y", "x")}
	if err := ValidatePlan(tasks); err == nil || !strings.Contains(err.Error(), "x -> y -> x") || strings.Contains(err.Error(), "root ->") {
		t.Errorf("err %v", err)
	}
}

func TestValidatePlanRejections(t *testing.T) {
	for name, c := range map[string]struct {
		tasks []TaskSpec
		want  string
	}{
		"none":              {nil, "no tasks"},
		"duplicate":         {[]TaskSpec{spec("a"), spec("a")}, "twice"},
		"missing":           {[]TaskSpec{spec("a", "ghost")}, `"ghost"`},
		"self":              {[]TaskSpec{spec("a", "a")}, "itself"},
		"repeated edge":     {[]TaskSpec{spec("a"), spec("b", "a", "a")}, "twice"},
		"uppercase id":      {[]TaskSpec{spec("Alpha")}, "task id"},
		"empty id":          {[]TaskSpec{spec("")}, "task id"},
		"empty output":      {[]TaskSpec{{ID: "a", Outputs: []string{""}}}, "empty"},
		"absolute path":     {[]TaskSpec{{ID: "a", Paths: []string{"/etc"}}}, "escapes"},
		"parent path":       {[]TaskSpec{{ID: "a", Paths: []string{"../x"}}}, "escapes"},
		"nested parent":     {[]TaskSpec{{ID: "a", Paths: []string{"a/../../x"}}}, "escapes"},
		"control character": {[]TaskSpec{{ID: "a", Paths: []string{"a\nb"}}}, "control"},
		"backslash":         {[]TaskSpec{{ID: "a", Paths: []string{`a\b`}}}, "escapes"},
	} {
		err := ValidatePlan(c.tasks)
		if err == nil || !strings.Contains(err.Error(), c.want) || !errors.Is(err, ErrInvalidTransition) {
			t.Errorf("%s: %v, want it to say %q", name, err, c.want)
		}
	}
}

func TestValidTaskIDMatchesTheStoresRule(t *testing.T) {
	for id, want := range map[string]bool{"a": true, "task-12": true, "receipt-store": true, "a1-b2": true, "": false, "1a": false, "A": false, "a_b": false, "a b": false, "-a": false, strings.Repeat("a", 64): true, strings.Repeat("a", 65): false} {
		if got := ValidTaskID(id); got != want {
			t.Errorf("%q: %v want %v", id, got, want)
		}
	}
}

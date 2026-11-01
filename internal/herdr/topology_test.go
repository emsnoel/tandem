package herdr

import (
	"errors"
	"testing"
)

func ag(pane, kind, name, tab string) Agent {
	a := Agent{PaneID: pane, Kind: kind, CWD: "/" + pane, TabID: tab}
	if name != "-" {
		a.Name = &name
	}
	return a
}

func TestPickPlannerPrefersFlag(t *testing.T) {
	agents := []Agent{ag("w1:p1", "claude", "", "t1"), ag("w1:p2", "claude", "x", "t2")}
	got, _, err := PickPlanner(agents, "w1:p2", "w1:p1", "claude")
	if err != nil || got.PaneID != "w1:p2" || got.TabID != "t2" || got.CWD != "/w1:p2" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

// Review focus 1: main carries on with empty fields for a pane Herdr does not list.
func TestPickPlannerFlagPaneNotListed(t *testing.T) {
	got, _, err := PickPlanner([]Agent{ag("w1:p1", "claude", "", "t1")}, "w9:p9", "", "claude")
	if err != nil || got.PaneID != "w9:p9" || got.CWD != "" || got.TabID != "" || got.Name != nil {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestPickPlannerCallerPaneOfKind(t *testing.T) {
	agents := []Agent{ag("w1:p1", "claude", "", "t1"), ag("w1:p2", "claude", "", "t1")}
	got, _, err := PickPlanner(agents, "", "w1:p2", "claude")
	if err != nil || got.PaneID != "w1:p2" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestPickPlannerCallerPaneOtherKindFallsBackToOnly(t *testing.T) {
	agents := []Agent{ag("w1:p1", "codex", "", "t1"), ag("w1:p2", "claude", "", "t1")}
	got, _, err := PickPlanner(agents, "", "w1:p1", "claude")
	if err != nil || got.PaneID != "w1:p2" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestPickPlannerNone(t *testing.T) {
	_, _, err := PickPlanner([]Agent{ag("w1:p1", "codex", "", "t1")}, "", "", "claude")
	if !errors.Is(err, ErrNoPlanner) {
		t.Fatalf("got %v, want ErrNoPlanner", err)
	}
}

func TestPickPlannerMany(t *testing.T) {
	agents := []Agent{ag("w1:p1", "claude", "", "t1"), ag("w1:p3", "codex", "", "t1"), ag("w1:p2", "claude", "b", "t1")}
	_, cands, err := PickPlanner(agents, "", "w1:p3", "claude")
	if !errors.Is(err, ErrManyPlanners) || len(cands) != 2 || cands[0].PaneID != "w1:p1" || cands[1].PaneID != "w1:p2" {
		t.Fatalf("got %+v, %v", cands, err)
	}
}

func TestAdoptableSingleUnnamed(t *testing.T) {
	agents := []Agent{ag("w1:p1", "claude", "planner", "t1"), ag("w1:p2", "codex", "-", "t1")}
	if a := Adoptable(agents, "w1:p1", "t1", ""); a == nil || a.PaneID != "w1:p2" || a.Kind != "codex" {
		t.Fatalf("got %+v", a)
	}
}

// Review focus 2: null and "" are both unnamed.
func TestAdoptableNullAndEmptyNameBothUnnamed(t *testing.T) {
	for _, name := range []string{"-", ""} {
		agents := []Agent{ag("w1:p2", "codex", name, "t1")}
		if a := Adoptable(agents, "w1:p1", "t1", ""); a == nil {
			t.Errorf("name %q: not adoptable", name)
		}
	}
	if a := Adoptable([]Agent{ag("w1:p2", "codex", "bob", "t1")}, "w1:p1", "t1", ""); a != nil {
		t.Errorf("a named agent was adoptable: %+v", a)
	}
}

func TestAdoptableSkipsExcludedPane(t *testing.T) {
	if a := Adoptable([]Agent{ag("w1:p1", "claude", "", "t1")}, "w1:p1", "t1", ""); a != nil {
		t.Fatalf("got %+v", a)
	}
}

// Review focus 3: an agent or a planner with no tab matches any tab.
func TestAdoptableTabRules(t *testing.T) {
	if a := Adoptable([]Agent{ag("w1:p2", "codex", "", "t2")}, "w1:p1", "t1", ""); a != nil {
		t.Errorf("other tab adopted: %+v", a)
	}
	if a := Adoptable([]Agent{ag("w1:p2", "codex", "", "")}, "w1:p1", "t1", ""); a == nil {
		t.Error("agent with no tab not adopted")
	}
	if a := Adoptable([]Agent{ag("w1:p2", "codex", "", "t2")}, "w1:p1", "", ""); a == nil {
		t.Error("planner with no tab did not adopt from another tab")
	}
}

func TestAdoptableKindFilter(t *testing.T) {
	agents := []Agent{ag("w1:p2", "codex", "", "t1"), ag("w1:p3", "claude", "", "t1")}
	if a := Adoptable(agents, "w1:p1", "t1", "claude"); a == nil || a.PaneID != "w1:p3" {
		t.Fatalf("got %+v", a)
	}
}

func TestAdoptableNoneWhenTwo(t *testing.T) {
	agents := []Agent{ag("w1:p2", "codex", "", "t1"), ag("w1:p3", "claude", "", "t1")}
	if a := Adoptable(agents, "w1:p1", "t1", ""); a != nil {
		t.Fatalf("got %+v", a)
	}
}

func at(pane, kind, cwd string) Agent { return Agent{PaneID: pane, Kind: kind, CWD: cwd} }

// Review focus 1: /x/repo-other is not under /x/repo; /x/repo/sub is.
func TestCountUnderPrefixIsNotChild(t *testing.T) {
	agents := []Agent{at("p1", "codex", "/x/repo-other"), at("p2", "codex", "/x/repo/sub"), at("p3", "codex", "/x/repo")}
	if got := CountUnder(agents, "codex", "/x/repo", nil); got != 2 {
		t.Fatalf("got %d, want 2", got)
	}
}

func TestCountUnderExcludesPanesAndOtherKinds(t *testing.T) {
	agents := []Agent{at("p1", "codex", "/r"), at("p2", "codex", "/r"), at("p3", "claude", "/r"), at("p4", "codex", "/r")}
	if got := CountUnder(agents, "codex", "/r", []string{"p1", "p4"}); got != 1 {
		t.Fatalf("got %d, want 1", got)
	}
}

func TestCountUnderEmptyExcludeIgnored(t *testing.T) {
	agents := []Agent{at("p1", "codex", "/r"), at("", "codex", "/r")}
	if got := CountUnder(agents, "codex", "/r", []string{""}); got != 2 {
		t.Fatalf("got %d, want 2: an empty exclude entry excludes nothing", got)
	}
}

func TestFindPane(t *testing.T) {
	agents := []Agent{at("p1", "codex", "/r"), at("p2", "claude", "/r")}
	if a := FindPane(agents, "p2"); a == nil || a.Kind != "claude" {
		t.Fatalf("got %+v", a)
	}
	if a := FindPane(agents, "p9"); a != nil {
		t.Fatalf("got %+v, want nil", a)
	}
}

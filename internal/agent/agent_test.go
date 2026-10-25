package agent

import "testing"

func TestDegradeReturnsRequestedWhenSupported(t *testing.T) {
	got, w, err := Degrade("autonomy", "auto", AutonomyOrder,
		[]string{"readonly", "ask", "auto", "full"}, nil, nil, "codex")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "auto" || w != nil {
		t.Fatalf("got (%q, %v), want (\"auto\", nil)", got, w)
	}
}

// readonly on an adapter whose lowest supported value is `ask` is a WIDENING:
// it grants write-with-prompts to an agent that asked to be read-only. That it
// also stalls an unattended pane does not make it a narrowing.
func TestDegradeRefusesWideningByDefault(t *testing.T) {
	_, _, err := Degrade("autonomy", "readonly", AutonomyOrder,
		[]string{"ask", "full"}, []string{"autonomy"}, nil, "opencode")
	if err == nil {
		t.Fatal("expected refusal; allow_degrade must not permit widening")
	}
	e, ok := err.(*Error)
	if !ok {
		t.Fatalf("want *Error, got %T", err)
	}
	if e.Exit != 21 || e.Direction != Widening || e.Nearest != "ask" {
		t.Fatalf("got exit=%d dir=%v nearest=%q, want 21/widening/ask", e.Exit, e.Direction, e.Nearest)
	}
}

func TestDegradeAllowsWideningWhenPermitted(t *testing.T) {
	got, w, err := Degrade("autonomy", "readonly", AutonomyOrder,
		[]string{"ask", "full"}, nil, []string{"autonomy"}, "opencode")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "ask" || w == nil || w.Direction != Widening {
		t.Fatalf("got (%q, %+v), want ask with a widening warning", got, w)
	}
}

// auto prefers the nearest value BELOW it, so it narrows to ask rather than
// escalating to full.
func TestDegradePrefersNearestBelow(t *testing.T) {
	got, w, err := Degrade("autonomy", "auto", AutonomyOrder,
		[]string{"ask", "full"}, []string{"autonomy"}, nil, "opencode")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "ask" || w == nil || w.Direction != Narrowing {
		t.Fatalf("got (%q, %+v), want ask with a narrowing warning", got, w)
	}
}

func TestDegradeRefusedNarrowingDoesNotEscalate(t *testing.T) {
	_, _, err := Degrade("autonomy", "auto", AutonomyOrder,
		[]string{"ask", "full"}, nil, []string{"autonomy"}, "opencode")
	if err == nil {
		t.Fatal("expected refusal: allow_widen must not satisfy a narrowing case")
	}
	if e := err.(*Error); e.Exit != 20 || e.Direction != Narrowing {
		t.Fatalf("got exit=%d dir=%v, want 20/narrowing", e.Exit, e.Direction)
	}
}

func TestDegradeEmptySupportedDropsAxisWhenPermitted(t *testing.T) {
	got, w, err := Degrade("effort", "high", EffortOrder, nil, []string{"effort"}, nil, "opencode")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" || w == nil || w.Direction != Narrowing {
		t.Fatalf("got (%q, %+v), want dropped axis with a narrowing warning", got, w)
	}
}

func TestDegradeEmptySupportedRefusesByDefault(t *testing.T) {
	if _, _, err := Degrade("effort", "high", EffortOrder, nil, nil, nil, "opencode"); err == nil {
		t.Fatal("expected refusal when the adapter has no support for the axis")
	}
}

func TestDegradeUnsetPassesThrough(t *testing.T) {
	got, w, err := Degrade("effort", "", EffortOrder, []string{"low"}, nil, nil, "codex")
	if err != nil || got != "" || w != nil {
		t.Fatalf("got (%q, %v, %v), want empty passthrough", got, w, err)
	}
}

func TestDegradeRejectsUnknownValue(t *testing.T) {
	_, _, err := Degrade("autonomy", "yolo", AutonomyOrder,
		[]string{"ask"}, []string{"autonomy"}, []string{"autonomy"}, "codex")
	if err == nil {
		t.Fatal("expected an error for a value outside the vocabulary")
	}
	if e := err.(*Error); e.Code != "autonomy_unknown" {
		t.Fatalf("got code %q, want autonomy_unknown", e.Code)
	}
}

func TestRoleUnattended(t *testing.T) {
	if !RoleBuilder.Unattended() || !RoleReviewer.Unattended() {
		t.Error("builder and reviewer have no human in front of them")
	}
	if RolePlanner.Unattended() {
		t.Error("a planner is somebody's own pane")
	}
}

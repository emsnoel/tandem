package herdr

import "errors"

// ErrNoPlanner and ErrManyPlanners are PickPlanner's two ways to find no
// planner: no agent of the kind runs, or several do and none is the caller.
var (
	ErrNoPlanner    = errors.New("no agent of the planner's kind is running")
	ErrManyPlanners = errors.New("several agents of the planner's kind are running")
)

// PickPlanner chooses the planner's pane, in bin/tandem-pair's order: the
// pane named on the command line, the calling pane if it runs kind, then the
// only agent of kind. A named pane Herdr does not list comes back with only
// its id. With several candidates it returns ErrManyPlanners and the
// candidates in list order.
func PickPlanner(agents []Agent, flagPane, callerPane, kind string) (Agent, []Agent, error) {
	if flagPane != "" {
		for _, a := range agents {
			if a.PaneID == flagPane {
				return a, nil, nil
			}
		}
		return Agent{PaneID: flagPane}, nil, nil
	}
	var ofKind []Agent
	for _, a := range agents {
		if a.Kind != kind {
			continue
		}
		if callerPane != "" && a.PaneID == callerPane {
			return a, nil, nil
		}
		ofKind = append(ofKind, a)
	}
	switch len(ofKind) {
	case 0:
		return Agent{}, nil, ErrNoPlanner
	case 1:
		return ofKind[0], nil, nil
	}
	return Agent{}, ofKind, ErrManyPlanners
}

// Adoptable returns the one unnamed agent bin/tandem-pair may adopt as the
// builder: not in excludePane, in tab (when either side has no tab, any tab
// matches), and of kind when kind is set. It returns nil for none or several,
// so a config change never starts a second builder beside a running one.
func Adoptable(agents []Agent, excludePane, tab, kind string) *Agent {
	var found *Agent
	for i := range agents {
		a := &agents[i]
		if a.PaneID == excludePane || (a.Name != nil && *a.Name != "") {
			continue
		}
		if tab != "" && a.TabID != "" && a.TabID != tab {
			continue
		}
		if kind != "" && a.Kind != kind {
			continue
		}
		if found != nil {
			return nil
		}
		found = a
	}
	return found
}

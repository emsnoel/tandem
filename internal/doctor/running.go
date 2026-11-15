package doctor

import (
	"strings"

	"github.com/emsnoel/tandem/internal/agent"
	"github.com/emsnoel/tandem/internal/herdr"
	"github.com/emsnoel/tandem/internal/pairname"
)

// roleNames lists the pair's roles with the name each holds.
func roleNames(n pairname.Names) []struct {
	role agent.Role
	name string
} {
	return []struct {
		role agent.Role
		name string
	}{{agent.RolePlanner, n.Planner}, {agent.RoleBuilder, n.Builder}, {agent.RoleReviewer, n.Reviewer}}
}

// sameCheckout reports whether dir is in the checkout root names. A folder that
// cannot be read counts as another checkout, as in tandem-pair.
func sameCheckout(root, dir string) bool {
	if dir == "" {
		return false
	}
	here, err := pairname.Checkout(root)
	if err != nil {
		return false
	}
	there, err := pairname.Checkout(dir)
	return err == nil && there == here
}

// RunningRoles finds a builder or reviewer that already runs in root's
// checkout, by the pair's names, and maps each to its pane. An agent with the
// name that works in another checkout is not this pair's, as tandem-pair does
// not adopt it. It returns none when Herdr cannot list its agents; the socket
// check reports that.
func RunningRoles(p Probe, root string, names pairname.Names) map[agent.Role]string {
	agents, err := bounded(p.Agents)
	if err != nil {
		return nil
	}
	out := map[agent.Role]string{}
	for _, r := range roleNames(names) {
		if r.role == agent.RolePlanner {
			continue
		}
		if a := herdr.Find(agents, r.name); a != nil && sameCheckout(root, a.CWD) {
			out[r.role] = a.PaneID
		}
	}
	return out
}

// PairNames reports the names this checkout pairs under, and warns for each
// live agent that holds one of them from another checkout.
func PairNames(p Probe, root string, names pairname.Names) []Result {
	rs := []Result{{Group: "repo", Name: "names", Status: OK,
		Detail: "this checkout pairs as " + names.Planner + ", " + names.Builder + ", " + names.Reviewer}}
	agents, err := bounded(p.Agents)
	if err != nil {
		return rs
	}
	for _, r := range roleNames(names) {
		a := herdr.Find(agents, r.name)
		if a == nil || sameCheckout(root, a.CWD) {
			continue
		}
		where := a.CWD
		if where == "" {
			where = "an unknown folder"
		}
		rs = append(rs, Result{Group: "repo", Name: "names:" + string(r.role), Status: Warn,
			Detail: "pane " + a.PaneID + " holds '" + r.name + "' but works in " + where +
				"; tandem-pair will refuse to adopt it",
			Fix: "close it from its own checkout with tandem-unpair, or pair here under other names (HERDR_" +
				strings.ToUpper(string(r.role)) + "_NAME)"})
	}
	return rs
}

// CallingPlannerKind is the kind of the agent in the calling pane when it can
// plan, which is the planner tandem-pair would adopt; else "".
func CallingPlannerKind(p Probe, getenv func(string) string) string {
	pane := getenv("HERDR_PANE_ID")
	if pane == "" {
		return ""
	}
	agents, err := bounded(p.Agents)
	if err != nil {
		return ""
	}
	if a := herdr.FindPane(agents, pane); a != nil && agent.CanPlan(a.Kind) {
		return a.Kind
	}
	return ""
}

// running reports each role in r.Running and what doctor leaves out for it.
func running(r Roles) []Result {
	skips := map[agent.Role]string{
		agent.RoleBuilder:  "its trust checks",
		agent.RoleReviewer: "its approval, trust and launch checks",
	}
	var rs []Result
	for _, role := range []agent.Role{agent.RoleBuilder, agent.RoleReviewer} {
		if pane, ok := r.Running[role]; ok {
			rs = append(rs, Result{Name: "running:" + string(role), Status: OK,
				Detail: string(role) + " already runs in pane " + pane + ", so doctor skips " +
					skips[role] + ", as tandem-pair does"})
		}
	}
	return rs
}

// starts reports whether tandem-pair would start role, so the checks it makes
// before a launch apply. The planner is never in Running, so its checks stay.
func (r Roles) starts(role agent.Role) bool {
	_, ok := r.Running[role]
	return !ok
}

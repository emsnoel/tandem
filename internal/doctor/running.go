package doctor

import (
	"github.com/emsnoel/tandem/internal/agent"
	"github.com/emsnoel/tandem/internal/herdr"
)

// RunningRoles finds a builder or reviewer that already runs, by the names
// tandem-pair gives them, and maps each to its pane. It returns none when Herdr
// cannot list its agents; the socket check reports that.
func RunningRoles(p Probe, getenv func(string) string) map[agent.Role]string {
	agents, err := bounded(p.Agents)
	if err != nil {
		return nil
	}
	out := map[agent.Role]string{}
	for role, name := range map[agent.Role]string{
		agent.RoleBuilder:  nameOr(getenv("HERDR_BUILDER_NAME"), "builder"),
		agent.RoleReviewer: nameOr(getenv("HERDR_REVIEWER_NAME"), "reviewer"),
	} {
		if a := herdr.Find(agents, name); a != nil {
			out[role] = a.PaneID
		}
	}
	return out
}

func nameOr(name, fallback string) string {
	if name == "" {
		return fallback
	}
	return name
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

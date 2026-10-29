// Package doctor finds setup problems before pairing. Every check calls the
// code tandem-pair and its subcommands already use, so doctor and a real launch
// cannot disagree about the same condition. Nothing here writes to disk.
package doctor

import (
	"sort"
	"strings"
	"time"

	"github.com/emsnoel/tandem/internal/agent"
)

// Status is one check's verdict. Only Fail makes doctor exit non-zero.
type Status string

const (
	OK   Status = "ok"
	Warn Status = "warn"
	Fail Status = "fail"
)

// Result is one check's finding. Fix is set whenever Status is not OK: the
// exact command or step that resolves it.
type Result struct {
	Group  string `json:"group"`
	Name   string `json:"name"`
	Status Status `json:"status"`
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

// Uses lists each agent kind the pair needs and the roles that use it.
type Uses map[string][]agent.Role

// probeTimeout bounds each call to herdr or an agent CLI, so a hung tool
// cannot hang doctor.
var probeTimeout = 2 * time.Second

var roleOrder = map[agent.Role]int{agent.RolePlanner: 0, agent.RoleBuilder: 1, agent.RoleReviewer: 2}

// kinds returns the kinds in the order their first role appears: planner,
// builder, reviewer. Output order never depends on map iteration.
func (u Uses) kinds() []string {
	out := make([]string, 0, len(u))
	for k := range u {
		out = append(out, k)
	}
	first := func(k string) int {
		n := len(roleOrder)
		for _, r := range u[k] {
			if roleOrder[r] < n {
				n = roleOrder[r]
			}
		}
		return n
	}
	sort.Slice(out, func(i, j int) bool {
		if a, b := first(out[i]), first(out[j]); a != b {
			return a < b
		}
		return out[i] < out[j]
	})
	return out
}

// roles names the roles using kind, such as "builder, reviewer".
func (u Uses) roles(kind string) string {
	rs := append([]agent.Role(nil), u[kind]...)
	sort.Slice(rs, func(i, j int) bool { return roleOrder[rs[i]] < roleOrder[rs[j]] })
	names := make([]string, len(rs))
	for i, r := range rs {
		names[i] = string(r)
	}
	return strings.Join(names, ", ")
}

// Count returns how many results failed and how many warned.
func Count(rs []Result) (failures, warnings int) {
	for _, r := range rs {
		switch r.Status {
		case Fail:
			failures++
		case Warn:
			warnings++
		}
	}
	return failures, warnings
}

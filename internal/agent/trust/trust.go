// Package trust holds every check tandem makes against another tool's own
// configuration before launching it. It is separate from internal/agent, which
// must stay pure; everything here touches the filesystem.
package trust

import (
	"sort"

	"github.com/emsnoel/tandem/internal/agent"
)

// Marker identifies entries tandem wrote. Anything without it was
// written by a human or an older version and is never modified or removed.
const Marker = "tandem:managed:trust"

// Gate is one launch-time check for one agent kind.
type Gate interface {
	// Kind is the agent kind this gate belongs to.
	Kind() string
	// Name identifies the gate within a kind, e.g. "project-trust".
	Name() string
	// Check inspects, and for an Apply gate may repair, the tool's own state.
	Check(cwd string, role agent.Role, opts Options) agent.Action
}

type Options struct {
	DryRun bool
	// Authorised reports that the user chose this agent kind for this repository,
	// by flag, environment, their own config, or an explicit approval. A gate that
	// writes another tool's config must check it, so a cloned repo can never trust
	// itself.
	Authorised bool
	// StrictHooks turns an unaccepted Codex hook from a warning into a refusal. Off
	// by default, because current Codex skips such a hook silently; older versions
	// prompted invisibly to Herdr. internal/hooks never reads this, so callers
	// choose the policy.
	StrictHooks bool
}

var registry = map[string][]Gate{}

func Register(g Gate) { registry[g.Kind()] = append(registry[g.Kind()], g) }

// noGates lists kinds that deliberately have no launch-time gate, with the
// reason. TestEveryAgentKindIsAccountedFor requires every kind to be here or in
// registry.
var noGates = map[string]string{}

// RegisterNoGates declares that kind has no launch-time trust or hook gate
// on purpose. Call it once, from that kind's own file in this package, the
// same way a real Gate calls Register from its own init().
func RegisterNoGates(kind, reason string) { noGates[kind] = reason }

// Check runs every gate for a kind. An unknown kind has no gates and returns
// nothing, which is the correct answer for opencode: it has no launch-time
// trust or hook gate at all.
func Check(kind, cwd string, role agent.Role) []agent.Action {
	return CheckWithOptions(kind, cwd, role, Options{})
}

// CheckWithOptions runs every gate with explicit execution options. Options
// are values rather than package globals so dry-run state cannot leak between
// calls in the same process.
func CheckWithOptions(kind, cwd string, role agent.Role, opts Options) []agent.Action {
	gates := registry[kind]
	out := make([]agent.Action, 0, len(gates))
	for _, g := range gates {
		out = append(out, g.Check(cwd, role, opts))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Gate < out[j].Gate })
	return out
}

// Kinds lists the kinds that have gates, for diagnostics.
func Kinds() []string {
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

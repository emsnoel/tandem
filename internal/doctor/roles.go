package doctor

import (
	"fmt"
	"os"

	"github.com/emsnoel/tandem/internal/agent"
	"github.com/emsnoel/tandem/internal/config"
	"github.com/emsnoel/tandem/internal/paths"
)

// PlannerKind is the planner tandem-pair adopts. bin/tandem-pair fixes it too.
const PlannerKind = "claude"

// RoleFlags are the overrides tandem-pair passes on its command line.
type RoleFlags struct{ Builder, Reviewer config.Overrides }

// Roles is what pairing would launch, resolved by the same code launch-spec
// uses.
type Roles struct {
	Planner, Builder config.Resolved
	Reviewer         config.ReviewerResolution
	// Manifest is the repo's .tandem.json, or "" when it has none.
	Manifest string
	// Warnings come from reading the manifest, such as unknown keys.
	Warnings []string
	// Running maps a builder or reviewer that already runs to its pane, as
	// RunningRoles finds them. tandem-pair skips some checks for those.
	Running map[agent.Role]string
	user    *config.User
}

// ResolveRoles reads both config sources and resolves all three roles. A
// manifest or user config that cannot be read is an error.
func ResolveRoles(root string, flags RoleFlags, getenv func(string) string) (Roles, error) {
	manifest := paths.Manifest(root)
	repo, warns, err := config.LoadRepo(manifest)
	if err != nil {
		return Roles{}, err
	}
	userPath := config.UserConfigPath()
	user, err := config.LoadUser(userPath)
	if err != nil {
		return Roles{}, fmt.Errorf("%s: %w", userPath, err)
	}
	if _, err := os.Stat(manifest); err != nil {
		manifest = ""
	}
	return Roles{
		Planner:  config.ResolveRole(agent.RolePlanner, repo, user, getenv, config.Overrides{Kind: PlannerKind}),
		Builder:  config.ResolveRole(agent.RoleBuilder, repo, user, getenv, flags.Builder),
		Reviewer: config.ResolveReviewer(repo, user, getenv, flags.Reviewer),
		Manifest: manifest,
		Warnings: warns,
		user:     user,
	}, nil
}

// launched lists the roles that would start, with their resolution. A
// reviewer that does not resolve starts nothing.
func (r Roles) launched() []config.Resolved {
	out := []config.Resolved{r.Planner, r.Builder}
	if r.Reviewer.Resolved != nil {
		out = append(out, *r.Reviewer.Resolved)
	}
	return out
}

// Uses lists each kind the pair needs and the roles that use it.
func (r Roles) Uses() Uses {
	u := Uses{}
	for _, res := range r.launched() {
		u[res.Kind.Value] = append(u[res.Kind.Value], res.Role)
	}
	return u
}

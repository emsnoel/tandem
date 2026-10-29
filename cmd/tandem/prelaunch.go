package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/emsnoel/tandem/internal/agent"
	"github.com/emsnoel/tandem/internal/agent/trust"
	"github.com/emsnoel/tandem/internal/config"
	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/paths"
)

// cmdPrelaunch runs every launch-time gate for one kind and decides whether the
// result is fatal for the role. A Manual status is a note for a planner and a
// refusal for a builder, which would otherwise sit at a dialog forever.
func cmdPrelaunch(args []string) int {
	fs := flag.NewFlagSet("agent prelaunch", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	role := fs.String("role", "builder", "planner|builder|reviewer")
	kind := fs.String("kind", "", "agent kind, as chosen by the launch spec")
	cwd := fs.String("cwd", "", "directory the agent will start in")
	root := fs.String("root", "", "repository root holding the manifest (default: --cwd)")
	dryRun := fs.Bool("dry-run", false, "report Apply gates without writing")
	userKind := fs.String("user-kind", "", "the kind named on tandem-pair's command line for this role, if any")
	if err := fs.Parse(args); err != nil {
		return exitcode.Usage
	}
	if *role != "planner" && *role != "builder" && *role != "reviewer" {
		fmt.Fprintf(os.Stderr, "tandem: --role must be planner, builder or reviewer, got %q\n", *role)
		return exitcode.Usage
	}
	if *kind == "" || *cwd == "" {
		fmt.Fprintln(os.Stderr, "usage: tandem agent prelaunch --role R --kind K --cwd DIR")
		return exitcode.Usage
	}

	actions := trust.CheckWithOptions(*kind, *cwd, agent.Role(*role), trust.Options{
		DryRun:     *dryRun,
		Authorised: kindAuthorised(*root, *cwd, *kind, *userKind, agent.Role(*role)),
		// Severity is the caller's decision, so the strict switch is read here.
		// tandem-pair's HERDR_SKIP_HOOK_CHECK overrides the exit code further out.
		StrictHooks: os.Getenv("HERDR_STRICT_HOOK_CHECK") == "1",
	})
	if rc := emitJSON(map[string]any{"actions": actions}); rc != exitcode.OK {
		return rc
	}

	for _, a := range actions {
		if !trust.Blocks(agent.Role(*role), a) {
			continue
		}
		fmt.Fprintf(os.Stderr,
			"tandem: %s gate %q needs a human, and a %s runs unattended.\n        %s\n        %s\n",
			*kind, a.Gate, *role, a.Detail, a.Remedy)
		return exitcode.ManualGate
	}
	return exitcode.OK
}

// kindAuthorised reports whether the user chose this kind for this repository,
// which an Apply gate needs before it writes another tool's config. It does
// not rely on launch-spec having refused first. Only a repo-requested kind the
// user has not approved is unauthorised; anything unreadable fails closed.
func kindAuthorised(root, cwd, kind, userKind string, role agent.Role) bool {
	if root == "" {
		root = cwd
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	repo, _, err := config.LoadRepo(paths.Manifest(absRoot))
	if err != nil {
		return false
	}
	user, err := config.LoadUser(config.UserConfigPath())
	if err != nil {
		return false
	}
	// A kind named on the command line resolves as the user's own choice, the
	// same way launch-spec and doctor see it.
	flags := config.Overrides{Kind: userKind}
	var r config.Resolved
	if role == agent.RoleReviewer {
		// A reviewer that does not resolve has no repo-chosen kind to challenge.
		res := config.ResolveReviewer(repo, user, os.Getenv, flags)
		if res.Resolved == nil {
			return true
		}
		r = *res.Resolved
	} else {
		r = config.ResolveRole(role, repo, user, os.Getenv, flags)
	}
	return r.Kind.Value != kind || !user.NeedsApproval(absRoot, r)
}

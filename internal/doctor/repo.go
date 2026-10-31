package doctor

import (
	"errors"
	"fmt"
	"strings"

	"github.com/emsnoel/tandem/internal/agent"
	"github.com/emsnoel/tandem/internal/agent/trust"
	"github.com/emsnoel/tandem/internal/config"
)

// Repo checks what this repository asks for: kind approvals, each agent's
// trust gates in cwd, the reviewer, and each role's launch settings.
func Repo(root, cwd string, r Roles, getenv func(string) string) []Result {
	var rs []Result
	if len(r.Warnings) > 0 {
		rs = append(rs, Result{Name: "config", Status: Warn,
			Detail: strings.Join(r.Warnings, "; "), Fix: "edit " + r.Manifest})
	}
	rs = append(rs, reviewer(r.Reviewer))
	rs = append(rs, running(r)...)
	// tandem-pair resolves the builder's launch spec, which checks approval and
	// the launch settings, even when the builder runs; a running reviewer gets
	// neither.
	for _, res := range r.launched() {
		if res.Role == agent.RoleBuilder || (res.Role == agent.RoleReviewer && r.starts(res.Role)) {
			rs = append(rs, approval(r.user, root, res))
		}
	}
	for _, kind := range r.Uses().kinds() {
		rs = append(rs, gates(r, root, cwd, kind, getenv)...)
	}
	for _, res := range r.launched() {
		if res.Role != agent.RoleReviewer || r.starts(res.Role) {
			rs = append(rs, launch(res, cwd))
		}
	}
	for i := range rs {
		rs[i].Group = "repo"
	}
	return rs
}

// from says where a kind came from, in words a person reads.
func from(v config.Value) string {
	switch v.From {
	case config.FromRepo:
		return "from .tandem.json"
	case config.FromUser:
		return "from your config"
	case config.FromEnv:
		return "from the environment"
	case config.FromFlag:
		return "from the command line"
	}
	return "by default"
}

func reviewer(rr config.ReviewerResolution) Result {
	if rr.Status == config.ReviewerResolved {
		return Result{Name: "reviewer", Status: OK,
			Detail: "reviewer: " + rr.Resolved.Kind.Value + " (" + from(rr.Resolved.Kind) + ")"}
	}
	// The "none" reason repeats the remedy and names a flag doctor lacks, so
	// it is not shown. An invalid reviewer's reason says what is wrong.
	detail := "no reviewer declared"
	if rr.Status == config.ReviewerInvalid {
		detail = "reviewer: " + rr.Reason
	}
	return Result{Name: "reviewer", Status: Fail, Detail: detail, Fix: config.NoReviewerRemedy}
}

func approval(user *config.User, root string, res config.Resolved) Result {
	r := Result{Name: "approval:" + string(res.Role)}
	kind := res.Kind.Value
	if user.NeedsApproval(root, res) {
		r.Status, r.Fix = Fail, "tandem-pair --approve-kind "+kind
		r.Detail = fmt.Sprintf("%s kind %s requested by .tandem.json, not approved", res.Role, kind)
		return r
	}
	r.Status, r.Detail = OK, fmt.Sprintf("%s kind %s (%s)", res.Role, kind, from(res.Kind))
	return r
}

// gates runs every trust gate for kind once, without writing. A gate reads
// the kind's own config for cwd, so the role changes only the grade: the
// strictest role using the kind decides it, as `tandem agent prelaunch` would.
// A running role has no prelaunch, so it neither grades nor needs the gates.
func gates(r Roles, root, cwd, kind string, getenv func(string) string) []Result {
	role, authorised, checked := agent.RolePlanner, true, false
	for _, res := range r.launched() {
		if res.Kind.Value != kind || !r.starts(res.Role) {
			continue
		}
		checked = true
		if res.Role.Unattended() {
			role = res.Role
		}
		if r.user.NeedsApproval(root, res) {
			authorised = false
		}
	}
	if !checked {
		return nil
	}
	roles := r.Uses().roles(kind)
	var rs []Result
	actions := trust.CheckWithOptions(kind, cwd, role, trust.Options{
		DryRun:      true,
		Authorised:  authorised,
		StrictHooks: getenv("HERDR_STRICT_HOOK_CHECK") == "1",
	})
	for _, a := range actions {
		res := Result{Name: "trust:" + kind + ":" + a.Gate,
			Detail: kind + " " + a.Gate + ": " + a.Detail + " (" + roles + ")", Fix: a.Remedy}
		switch {
		case a.Status == agent.StatusAbsent:
			// The CLI check already reports a missing tool.
			continue
		case trust.Blocks(role, a) && getenv("HERDR_SKIP_HOOK_CHECK") == "1":
			// tandem-pair launches past any gate refusal with this set.
			res.Status = Warn
			res.Detail += "; HERDR_SKIP_HOOK_CHECK=1 lets tandem-pair launch anyway"
		case trust.Blocks(role, a):
			res.Status = Fail
		case a.Status == agent.StatusManual, a.Status == agent.StatusWarning, a.Status == agent.StatusFailed:
			// A gate that could not read its tool's config does not stop
			// prelaunch either.
			res.Status = Warn
		default:
			res.Status, res.Fix = OK, ""
		}
		if res.Status != OK && res.Fix == "" {
			res.Fix = "see " + a.Target
		}
		rs = append(rs, res)
	}
	return rs
}

// launch maps res to its CLI's flags, the same step `tandem agent
// launch-spec` takes, and reports a refusal (exits 20 and 21) before pairing.
func launch(res config.Resolved, cwd string) Result {
	r := Result{Name: "launch:" + string(res.Role)}
	a, err := agent.Get(res.Kind.Value)
	if err == nil {
		var l agent.Launch
		l, err = a.Resolve(res.Spec(cwd))
		if err == nil && len(l.Warnings) > 0 {
			var notes []string
			for _, w := range l.Warnings {
				notes = append(notes, fmt.Sprintf("%s=%s runs as %s", w.Axis, w.Requested, w.Substituted))
			}
			r.Status = Warn
			r.Detail = res.Kind.Value + " " + string(res.Role) + " settings: " + strings.Join(notes, ", ")
			r.Fix = "set a value " + res.Kind.Value + " supports, or keep the substitution"
			return r
		}
	}
	var refusal *agent.Error
	if errors.As(err, &refusal) {
		r.Status, r.Fix = Fail, refusal.Remedy
		r.Detail = fmt.Sprintf("%s %s settings refused: %s=%s (exit %d)",
			res.Kind.Value, res.Role, refusal.Axis, refusal.Value, refusal.Exit)
		return r
	}
	if err != nil {
		r.Status, r.Detail, r.Fix = Fail, err.Error(), "check the "+string(res.Role)+" settings"
		return r
	}
	r.Status, r.Detail = OK, res.Kind.Value+" "+string(res.Role)+" settings map to its CLI"
	return r
}

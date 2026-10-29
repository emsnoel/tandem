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
	for _, res := range r.launched() {
		if res.Role != agent.RolePlanner {
			rs = append(rs, approval(r.user, root, res))
		}
	}
	for _, res := range r.launched() {
		rs = append(rs, gates(r.user, root, cwd, res, getenv)...)
	}
	for _, res := range r.launched() {
		rs = append(rs, launch(res, cwd))
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
	return Result{Name: "reviewer", Status: Fail,
		Detail: "reviewer is " + string(rr.Status) + ": " + rr.Reason, Fix: config.NoReviewerRemedy}
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

// gates runs every trust gate for res's kind without writing, and grades each
// finding the way `tandem agent prelaunch` does.
func gates(user *config.User, root, cwd string, res config.Resolved, getenv func(string) string) []Result {
	var rs []Result
	actions := trust.CheckWithOptions(res.Kind.Value, cwd, res.Role, trust.Options{
		DryRun:      true,
		Authorised:  !user.NeedsApproval(root, res),
		StrictHooks: getenv("HERDR_STRICT_HOOK_CHECK") == "1",
	})
	for _, a := range actions {
		r := Result{Name: "trust:" + string(res.Role) + ":" + a.Gate,
			Detail: res.Kind.Value + " " + a.Gate + ": " + a.Detail, Fix: a.Remedy}
		switch {
		case a.Status == agent.StatusAbsent:
			// The CLI check already reports a missing tool.
			continue
		case trust.Blocks(res.Role, a), a.Status == agent.StatusFailed:
			r.Status = Fail
		case a.Status == agent.StatusManual, a.Status == agent.StatusWarning:
			r.Status = Warn
		default:
			r.Status, r.Fix = OK, ""
		}
		if r.Status != OK && r.Fix == "" {
			r.Fix = "see " + a.Target
		}
		rs = append(rs, r)
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

package trust

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/emsnoel/tandem/internal/agent"
)

func init() { Register(claudeProjectTrust{}) }

// claudeProjectTrust reports whether Claude Code's trust dialog has been
// accepted for a directory. Report only: ~/.claude.json is application state
// every live session rewrites, so a read-modify-write would race them.
type claudeProjectTrust struct{}

func (claudeProjectTrust) Kind() string { return "claude" }
func (claudeProjectTrust) Name() string { return "project-trust" }

func (g claudeProjectTrust) Check(cwd string, _ agent.Role, _ Options) agent.Action {
	home, err := os.UserHomeDir()
	a := agent.Action{
		Gate: g.Name(), Disposition: agent.Report, DispositionS: "report",
	}
	set := func(status agent.Status, detail, remedy string) agent.Action {
		a.Status, a.StatusS, a.Detail, a.Remedy = status, status.String(), detail, remedy
		return a
	}
	if err != nil {
		return set(agent.StatusFailed, "could not locate the home directory", "")
	}
	path := filepath.Join(home, ".claude.json")
	a.Target = path

	key, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		key = cwd
	}

	remedy := "run `claude` once in " + key + " and accept the trust dialog, then re-pair. " +
		"tandem will not write ~/.claude.json: Claude Code owns and rewrites it."

	body, err := os.ReadFile(path)
	if err != nil {
		// Missing is not a failure; Claude Code has simply never run here.
		return set(agent.StatusManual, "no ~/.claude.json yet", remedy)
	}

	var doc struct {
		Projects map[string]struct {
			HasTrustDialogAccepted bool `json:"hasTrustDialogAccepted"`
		} `json:"projects"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return set(agent.StatusFailed, "could not parse "+path,
			"tandem will not repair this file; check it by hand")
	}
	if p, ok := doc.Projects[key]; ok && p.HasTrustDialogAccepted {
		return set(agent.StatusCurrent, "claude already trusts "+key, "")
	}
	return set(agent.StatusManual, "claude has no accepted trust dialog for "+key, remedy)
}

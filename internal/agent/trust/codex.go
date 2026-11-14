package trust

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/emsnoel/tandem/internal/agent"
)

func init() { Register(codexProjectTrust{}) }

// codexProjectTrust records a directory in Codex's trusted-projects list, so an
// unattended builder does not stall at the trust prompt. Apply rather than
// Report: config.toml is a plain file humans edit, and appending a table is how
// Codex records trust itself.
type codexProjectTrust struct{}

func (codexProjectTrust) Kind() string { return "codex" }
func (codexProjectTrust) Name() string { return "project-trust" }

func (g codexProjectTrust) Check(cwd string, _ agent.Role, opts Options) agent.Action {
	a := agent.Action{
		Gate: g.Name(), Disposition: agent.Apply, DispositionS: "apply",
		Target: codexConfigPath(),
	}
	fail := func(status agent.Status, detail, remedy string) agent.Action {
		a.Status, a.StatusS, a.Detail, a.Remedy = status, status.String(), detail, remedy
		return a
	}

	// Codex stores the resolved physical path, so a symlinked cwd would not
	// match what it looks up.
	key, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return fail(agent.StatusFailed, "could not resolve "+cwd,
			"check that the directory exists and is readable")
	}
	a.Detail = key

	// A TOML basic-string key cannot hold a raw quote or backslash, and this path
	// is not ours to control.
	if strings.ContainsAny(key, `"\`) {
		return fail(agent.StatusManual,
			key+" contains a quote or backslash and cannot be a TOML key",
			"trust this directory manually in Codex, or pair from a path without those characters")
	}

	cfgPath := a.Target
	if cfgPath == "" {
		return fail(agent.StatusFailed, "could not locate Codex's config", "set CODEX_HOME")
	}

	body, err := os.ReadFile(cfgPath)
	if err != nil && !os.IsNotExist(err) {
		return fail(agent.StatusFailed, "could not read "+cfgPath,
			"check its permissions, or trust the directory manually in Codex")
	}
	if hasProjectEntry(string(body), key) {
		return fail(agent.StatusCurrent, "codex already trusts "+key, "")
	}
	if opts.DryRun {
		return fail(agent.StatusApplied,
			fmt.Sprintf("would append %s trust_level = \"trusted\" to %s", projectHeader(key), cfgPath), "")
	}

	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
		return fail(agent.StatusFailed, "could not create "+filepath.Dir(cfgPath), "check its permissions")
	}
	f, err := os.OpenFile(cfgPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fail(agent.StatusFailed, "could not write "+cfgPath,
			"check its permissions, or trust the directory manually in Codex")
	}
	defer f.Close()

	// Appending is safe: a new table header closes whatever table preceded it.
	// The marker goes on the line above so removeManagedEntry can find it.
	entry := fmt.Sprintf("\n# %s\n%s\ntrust_level = \"trusted\"\n", Marker, projectHeader(key))
	if _, err := f.WriteString(entry); err != nil {
		return fail(agent.StatusFailed, "could not append to "+cfgPath, "check its permissions")
	}
	return fail(agent.StatusApplied, "recorded Codex trust for "+key, "")
}

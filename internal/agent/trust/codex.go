package trust

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/emsnoel/tandem/internal/agent"
	"github.com/emsnoel/tandem/internal/hooks"
	"github.com/emsnoel/tandem/internal/paths"
)

func init() { Register(codexProjectTrust{}) }
func init() { Register(codexHookAcceptance{}) }

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

// codexHookAcceptance reports hook sources Codex has not accepted.
//
// Report, never Apply: Codex's trusted_hash is over an internal canonical form
// that a reimplementation would break on upgrade.
//
// Warning, not Manual, unless Options.StrictHooks is set: Codex skips an
// unaccepted hook silently, so it cannot stall the pane. Older versions
// prompted, so the strict flag keeps the refusal. No version that still
// prompts is known, so the warning names the installed `codex --version`
// beside the versions confirmed.
type codexHookAcceptance struct{}

// codexVersion returns the trimmed `codex --version` output, or "" if it cannot
// be read. It only adds to the message and never changes the verdict.
func codexVersion() string {
	out, err := exec.Command("codex", "--version").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (codexHookAcceptance) Kind() string { return "codex" }
func (codexHookAcceptance) Name() string { return "hook-acceptance" }

func (g codexHookAcceptance) Check(_ string, _ agent.Role, opts Options) agent.Action {
	home := paths.CodexHome()
	a := agent.Action{
		Gate: g.Name(), Disposition: agent.Report, DispositionS: "report",
		Target: home,
	}
	set := func(status agent.Status, detail, remedy string) agent.Action {
		a.Status, a.StatusS, a.Detail, a.Remedy = status, status.String(), detail, remedy
		return a
	}
	if home == "" {
		return set(agent.StatusAbsent, "could not locate Codex's home", "")
	}

	// A dry run must leave Codex's home untouched, baseline included.
	classify := hooks.Classify
	if opts.DryRun {
		classify = hooks.Inspect
	}
	findings := classify(home)
	if len(findings) == 0 {
		return set(agent.StatusCurrent, "no unaccepted hook sources", "")
	}

	paths := make([]string, 0, len(findings))
	for _, f := range findings {
		paths = append(paths, f.Kind+" "+f.Path)
	}
	detail := fmt.Sprintf("%d unaccepted hook source(s): %s", len(findings), strings.Join(paths, "; "))

	measured := "measured against codex-cli 0.151.0 and 0.152.1"
	if v := codexVersion(); v != "" {
		measured += "; the installed CLI reports \"" + v + "\""
	} else {
		measured += "; the installed CLI's version could not be read"
	}

	// Both statuses carry the same remedy: what clears the finding does not
	// depend on how severely this launch treats it.
	remedy := "Codex silently SKIPS an unaccepted hook rather than prompting, so the builder " +
		"runs with those hooks inert while looking healthy (" + measured + " -- if the " +
		"installed version differs from what was measured, verify its actual behavior " +
		"before trusting this warning over HERDR_STRICT_HOOK_CHECK=1's refusal). Accept " +
		"them in Codex with /hooks; or add \"--dangerously-bypass-hook-trust\" to " +
		"adapters.codex.args in ~/.config/herdr/tandem.json to run them unaccepted, which " +
		"trusts every hook source Codex loads."
	if opts.StrictHooks {
		return set(agent.StatusManual, detail,
			remedy+" HERDR_SKIP_HOOK_CHECK=1 launches anyway, with the hooks still skipped.")
	}
	return set(agent.StatusWarning, detail, remedy)
}

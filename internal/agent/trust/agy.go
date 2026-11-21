package trust

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/emsnoel/tandem/internal/agent"
	"github.com/emsnoel/tandem/internal/paths"
)

func init() { Register(agyProjectTrust{}) }

// agyProjectTrust records a directory in agy's trustedWorkspaces list, so an
// unattended agy does not stall at the folder-trust dialog.
//
// It applies only when the user authorised this kind for this repository (see
// Options.Authorised). Unauthorised, it only reports.
//
// settings.json is plain JSON. Every key tandem does not own is carried as
// json.RawMessage and written back as the exact bytes it was read as.
type agyProjectTrust struct{}

func (agyProjectTrust) Kind() string { return "agy" }
func (agyProjectTrust) Name() string { return "project-trust" }

func (g agyProjectTrust) Check(cwd string, _ agent.Role, opts Options) agent.Action {
	disposition, dispositionS := agent.Report, "report"
	if opts.Authorised {
		disposition, dispositionS = agent.Apply, "apply"
	}
	a := agent.Action{Gate: g.Name(), Disposition: disposition, DispositionS: dispositionS}
	set := func(status agent.Status, detail, remedy string) agent.Action {
		a.Status, a.StatusS, a.Detail, a.Remedy = status, status.String(), detail, remedy
		return a
	}

	home := paths.AgyHome()
	if home == "" {
		return set(agent.StatusAbsent, "could not locate agy's home", "set HOME")
	}
	path := filepath.Join(home, "settings.json")
	a.Target = path

	key := cwd
	if resolved, err := filepath.EvalSymlinks(cwd); err == nil {
		key = resolved
	}

	remedy := "run `agy` once in " + key + " and choose \"Yes, I trust this folder\"."

	body, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return set(agent.StatusFailed, "could not read "+path,
			"check its permissions, or trust the directory manually in agy")
	}
	doc, trusted, perr := parseAgySettings(body)
	if perr != nil {
		return set(agent.StatusFailed, "could not parse "+path,
			"tandem will not repair this file; check it by hand")
	}
	for _, f := range trusted {
		if f == key || f == cwd {
			return set(agent.StatusCurrent, "agy already trusts "+key, "")
		}
	}

	if !opts.Authorised {
		return set(agent.StatusManual, "agy has no trusted-workspace entry for "+key,
			remedy+" tandem will not add it for you: this repository asked for the "+
				"agy builder and you have not approved that here. Approve it once with "+
				"`tandem-pair --approve-kind agy` and tandem will record the trust itself.")
	}

	if opts.DryRun {
		return set(agent.StatusApplied, "would add "+key+" to trustedWorkspaces in "+path, "")
	}

	if err := os.MkdirAll(home, 0o700); err != nil {
		return set(agent.StatusFailed, "could not create "+home, "check its permissions")
	}
	if err := writeAgySettings(path, doc, append(trusted, key)); err != nil {
		return set(agent.StatusFailed, "could not write "+path,
			"check its permissions, or trust the directory manually in agy")
	}
	ledger := paths.AgyTrustLedger(home)
	if err := appendLedger(ledger, key); err != nil {
		return set(agent.StatusApplied,
			"recorded agy trust for "+key+", but could not update "+ledger+
				"; --clean-trust will not remove it", "")
	}
	return set(agent.StatusApplied, "recorded agy trust for "+key, "")
}

// parseAgySettings returns settings.json's keys as raw bytes and the
// trustedWorkspaces list. Empty input is the "agy has never run here" case.
func parseAgySettings(body []byte) (map[string]json.RawMessage, []string, error) {
	doc := map[string]json.RawMessage{}
	if len(strings.TrimSpace(string(body))) == 0 {
		return doc, nil, nil
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, nil, err
	}
	var trusted []string
	if raw, ok := doc["trustedWorkspaces"]; ok {
		if err := json.Unmarshal(raw, &trusted); err != nil {
			return nil, nil, err
		}
	}
	return doc, trusted, nil
}

// writeAgySettings re-emits settings.json with trustedWorkspaces replaced, via
// a temp file and rename. Other keys keep the bytes they were read as.
func writeAgySettings(path string, doc map[string]json.RawMessage, trusted []string) error {
	if trusted == nil {
		trusted = []string{}
	}
	list, err := json.Marshal(trusted)
	if err != nil {
		return err
	}
	doc["trustedWorkspaces"] = list

	keys := make([]string, 0, len(doc))
	for k := range doc {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteString("{\n")
	for i, k := range keys {
		name, err := json.Marshal(k)
		if err != nil {
			return err
		}
		b.WriteString("  " + string(name) + ": " + string(doc[k]))
		if i < len(keys)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString("}\n")

	tmp := path + ".tandem.tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// cleanAgy removes the trustedWorkspaces entry tandem added for cwd. An entry
// missing from the ledger is the human's and stays.
func cleanAgy(cwd string) (bool, error) {
	home := paths.AgyHome()
	if home == "" {
		return false, nil
	}
	ledgerPath := paths.AgyTrustLedger(home)
	ledger := readLedger(ledgerPath)
	if len(ledger) == 0 {
		return false, nil
	}
	key, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		key = cwd
	}
	if !containsPath(ledger, key) && !containsPath(ledger, cwd) {
		return false, nil
	}

	path := filepath.Join(home, "settings.json")
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	doc, trusted, err := parseAgySettings(body)
	if err != nil {
		return false, err
	}
	kept := make([]string, 0, len(trusted))
	removed := false
	for _, f := range trusted {
		if f == key || f == cwd {
			removed = true
			continue
		}
		kept = append(kept, f)
	}
	if !removed {
		return false, nil
	}
	if err := writeAgySettings(path, doc, kept); err != nil {
		return false, err
	}

	var rest []string
	for _, l := range ledger {
		if l != key && l != cwd {
			rest = append(rest, l)
		}
	}
	out := []byte(strings.Join(rest, "\n"))
	if len(rest) > 0 {
		out = append(out, '\n')
	}
	if err := os.WriteFile(ledgerPath, out, 0o600); err != nil {
		return true, err
	}
	return true, nil
}

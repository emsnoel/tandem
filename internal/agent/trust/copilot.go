package trust

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/emsnoel/tandem/internal/agent"
	"github.com/emsnoel/tandem/internal/paths"
)

func init() { Register(copilotProjectTrust{}) }

// copilotProjectTrust records a directory in Copilot's trusted-folders list, so
// an unattended builder does not stall at the trust prompt. No flag suppresses
// that prompt, so writing the config is the only way.
//
// It applies only when the user authorised this kind for this repository (see
// Options.Authorised): a repository may declare intent, but only a user grants
// capability. Unauthorised, it only reports. The grant is narrower than the
// --allow-all-tools the user already allows for the builder.
//
// The write keeps Copilot's leading // banner verbatim and carries every other
// key as json.RawMessage, so unknown values are preserved byte for byte. It can
// race another live Copilot session, which costs at most one extra dialog.
type copilotProjectTrust struct{}

func (copilotProjectTrust) Kind() string { return "copilot" }
func (copilotProjectTrust) Name() string { return "project-trust" }

func (g copilotProjectTrust) Check(cwd string, _ agent.Role, opts Options) agent.Action {
	disposition, dispositionS := agent.Report, "report"
	if opts.Authorised {
		disposition, dispositionS = agent.Apply, "apply"
	}
	a := agent.Action{Gate: g.Name(), Disposition: disposition, DispositionS: dispositionS}
	set := func(status agent.Status, detail, remedy string) agent.Action {
		a.Status, a.StatusS, a.Detail, a.Remedy = status, status.String(), detail, remedy
		return a
	}

	home := paths.CopilotHome()
	if home == "" {
		return set(agent.StatusAbsent, "could not locate Copilot's home", "set COPILOT_HOME")
	}
	path := filepath.Join(home, "config.json")
	a.Target = path

	// It is unknown whether Copilot stores the resolved path, so both forms are
	// accepted on read. The resolved form is written.
	key := cwd
	if resolved, err := filepath.EvalSymlinks(cwd); err == nil {
		key = resolved
	}

	remedy := "run `copilot` once in " + key + " and answer \"Yes, and remember this folder " +
		"for future sessions\" -- plain \"Yes\" trusts that session only and records nothing, " +
		"so this gate would refuse again."

	body, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return set(agent.StatusFailed, "could not read "+path,
			"check its permissions, or trust the directory manually in Copilot")
	}

	banner, doc, trusted, perr := parseCopilotConfig(body)
	if perr != nil {
		return set(agent.StatusFailed, "could not parse "+path,
			"tandem will not repair this file; check it by hand")
	}
	for _, f := range trusted {
		if f == key || f == cwd {
			return set(agent.StatusCurrent, "copilot already trusts "+key, "")
		}
	}

	if !opts.Authorised {
		// The kind came from the repository and the user has not approved it
		// here. Say so rather than trusting a folder on a repo's say-so.
		return set(agent.StatusManual, "copilot has no trusted-folder entry for "+key,
			remedy+" tandem will not add it for you: this repository asked for the "+
				"copilot builder and you have not approved that here. Approve it once with "+
				"`tandem-pair --approve-kind copilot` and tandem will record the trust itself.")
	}

	if opts.DryRun {
		return set(agent.StatusApplied, "would add "+key+" to trustedFolders in "+path, "")
	}

	if err := os.MkdirAll(home, 0o700); err != nil {
		return set(agent.StatusFailed, "could not create "+home, "check its permissions")
	}
	if err := writeCopilotConfig(path, banner, doc, append(trusted, key)); err != nil {
		return set(agent.StatusFailed, "could not write "+path,
			"check its permissions, or trust the directory manually in Copilot")
	}
	if err := appendLedger(paths.CopilotTrustLedger(home), key); err != nil {
		// The trust landed; only the record of who added it did not. Say so,
		// because it means --clean-trust will leave the entry alone later.
		return set(agent.StatusApplied,
			"recorded Copilot trust for "+key+", but could not update "+paths.CopilotTrustLedger(home)+
				"; --clean-trust will not remove it", "")
	}
	return set(agent.StatusApplied, "recorded Copilot trust for "+key, "")
}

// parseCopilotConfig splits Copilot's JSONC config into its leading comment
// banner, its remaining keys as raw bytes, and the trustedFolders list.
//
// Empty input is not an error: it is the "Copilot has never run here" case,
// and yields an empty document a write can still create.
func parseCopilotConfig(body []byte) (banner string, doc map[string]json.RawMessage, trusted []string, err error) {
	doc = map[string]json.RawMessage{}
	if len(strings.TrimSpace(string(body))) == 0 {
		return "", doc, nil, nil
	}

	lines := strings.Split(string(body), "\n")
	var head []string
	rest := 0
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "//") {
			head = append(head, l)
			continue
		}
		if strings.TrimSpace(l) == "" && len(head) > 0 {
			head = append(head, l)
			continue
		}
		rest = i
		break
	}
	banner = strings.Join(head, "\n")
	if banner != "" {
		banner += "\n"
	}

	if err := json.Unmarshal([]byte(strings.Join(lines[rest:], "\n")), &doc); err != nil {
		return "", nil, nil, err
	}
	if raw, ok := doc["trustedFolders"]; ok {
		if err := json.Unmarshal(raw, &trusted); err != nil {
			return "", nil, nil, err
		}
	}
	return banner, doc, trusted, nil
}

// writeCopilotConfig re-emits the config with trustedFolders replaced, via a
// temp file and rename so an interrupted write cannot truncate Copilot's
// config. Every other key is written back as the exact bytes it was read as.
func writeCopilotConfig(path, banner string, doc map[string]json.RawMessage, trusted []string) error {
	folders, err := json.Marshal(trusted)
	if err != nil {
		return err
	}
	doc["trustedFolders"] = folders

	keys := make([]string, 0, len(doc))
	for k := range doc {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteString(banner)
	b.WriteString("{\n")
	for i, k := range keys {
		name, err := json.Marshal(k)
		if err != nil {
			return err
		}
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, doc[k], "  ", "  "); err != nil {
			// Not indentable (a scalar); write it as-is.
			pretty.Reset()
			pretty.Write(doc[k])
		}
		b.WriteString("  " + string(name) + ": " + pretty.String())
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

// appendLedger records one path as tandem's own, so Clean can tell it apart
// from a folder the human trusted by hand.
func appendLedger(path, key string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(key + "\n")
	return err
}

// readLedger returns the paths tandem recorded, or nil when there are none.
func readLedger(path string) []string {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []string
	for _, l := range strings.Split(string(body), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

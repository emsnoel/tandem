package config

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
)

// Project is a repository's resolved settings: detected from its files, then
// overlaid by its manifest. bin/tandem-project prints it as JSON.
type Project struct {
	Root, PlansDir, CheckCommand, ConventionsFile, BuilderKind, PromptExtra string
	BuildEngine, ReviewDelivery, AcceptancePolicy, Collaboration            string
	// Source is "manifest" when a manifest supplied values, else "detected".
	Source string
	// InvalidManifest is the manifest's path when it exists but is not a JSON
	// object. Nothing is read from it; the caller says so.
	InvalidManifest string
	// CheckPaths limits the check's tree fingerprint. Empty means the whole tree.
	CheckPaths                    []string
	TaskInputBudget, ContextLimit int64
}

// DefaultTaskInputBudget is the input tokens one build task may use before
// tandem stops the build for a person to look. 0 turns it off.
const DefaultTaskInputBudget = 3000000

// ResolveProject resolves root's settings with the manifest at manifest, in
// the order bin/tandem-project always used. It never fails: a missing manifest
// leaves detection, and one that is not a JSON object is reported in
// InvalidManifest and otherwise ignored.
func ResolveProject(root, manifest string) Project {
	p := Project{
		Root: root, PlansDir: firstDir(root, "docs/superpowers/plans", "docs/plans", "plans", ".plans"),
		ConventionsFile: firstFile(root, "AGENTS.md", "CONTRIBUTING.md"), BuilderKind: "codex",
		TaskInputBudget: DefaultTaskInputBudget, Source: "detected",
	}
	raw, present, ok := readManifest(manifest)
	if present && !ok {
		p.InvalidManifest = manifest
	}
	if ok {
		p.Source = "manifest"
		str := func(k string) string {
			var s string
			_ = json.Unmarshal(raw[k], &s)
			return strings.TrimRight(s, "\n")
		}
		for k, dst := range map[string]*string{
			"plans_dir": &p.PlansDir, "conventions_file": &p.ConventionsFile,
			"builder_kind": &p.BuilderKind, "prompt_extra": &p.PromptExtra,
		} {
			if v := str(k); v != "" {
				*dst = v
			}
		}
		p.BuildEngine, p.ReviewDelivery = str("build_engine"), str("review_delivery")
		p.AcceptancePolicy, p.Collaboration = str("acceptance_policy"), str("collaboration")
		// Only a number replaces the default, so a typo cannot turn the budget off.
		if n, ok := wholeNumber(raw["task_input_budget"]); ok {
			p.TaskInputBudget = n
		}
		if n, ok := wholeNumber(raw["context_limit"]); ok {
			p.ContextLimit = n
		}
	}
	if !ok {
		manifest = "" // ResolveCheck must not read what was just set aside
	}
	c := ResolveCheck(root, manifest)
	p.CheckCommand, p.CheckPaths = c.Command, c.Paths
	return p
}

// readManifest reports whether the file exists and whether it is a JSON object.
func readManifest(path string) (map[string]json.RawMessage, bool, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, !os.IsNotExist(err), false
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(b, &raw) != nil || raw == nil {
		return nil, true, false
	}
	return raw, true, true
}

// wholeNumber is a non-negative JSON number, floored, as jq's
// `numbers | floor` and a ^[0-9]+$ check gave.
func wholeNumber(v json.RawMessage) (int64, bool) {
	v = bytes.TrimSpace(v)
	// A quoted "9" decodes into json.Number too, but jq's `numbers` drops it.
	if len(v) == 0 || (v[0] != '-' && (v[0] < '0' || v[0] > '9')) {
		return 0, false
	}
	var n json.Number
	if json.Unmarshal(v, &n) != nil {
		return 0, false
	}
	f, err := n.Float64()
	if err != nil || f < 0 || f >= math.MaxInt64 {
		return 0, false
	}
	return int64(math.Floor(f)), true
}

func firstDir(root string, names ...string) string {
	for _, n := range names {
		if st, err := os.Stat(filepath.Join(root, n)); err == nil && st.IsDir() {
			return n
		}
	}
	return ""
}

func firstFile(root string, names ...string) string {
	for _, n := range names {
		if isFile(filepath.Join(root, n)) {
			return n
		}
	}
	return ""
}

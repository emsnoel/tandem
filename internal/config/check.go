package config

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Check is the project's check command and the paths its result covers. Empty
// Paths means the whole tree.
type Check struct {
	Command string
	Paths   []string
}

// ResolveCheck reads check_command and check_paths from the manifest at
// manifest, filling the command from DetectCheckCommand(root) when the
// manifest has none. It reads leniently and never fails: a missing or invalid
// manifest, a non-string command or a non-array check_paths is ignored. It
// does not use LoadRepo, which refuses a manifest with a bad build_engine.
func ResolveCheck(root, manifest string) Check {
	var c Check
	var raw map[string]json.RawMessage
	if b, err := os.ReadFile(manifest); err == nil && json.Unmarshal(b, &raw) == nil {
		_ = json.Unmarshal(raw["check_command"], &c.Command)
		// tandem-project read it through $(...), which stripped trailing newlines.
		c.Command = strings.TrimRight(c.Command, "\n")
		var items []json.RawMessage
		if json.Unmarshal(raw["check_paths"], &items) == nil {
			for _, it := range items {
				var p string
				if json.Unmarshal(it, &p) == nil && p != "" {
					c.Paths = append(c.Paths, p)
				}
			}
		}
	}
	if c.Command == "" {
		c.Command = DetectCheckCommand(root)
	}
	return c
}

// DetectCheckCommand guesses the check command from root's build files,
// preferring a target the repo declares over a guess that will not run.
// "" when nothing matches.
func DetectCheckCommand(root string) string {
	if b, err := os.ReadFile(filepath.Join(root, "package.json")); err == nil {
		var pkg struct {
			Scripts map[string]json.RawMessage `json:"scripts"`
		}
		if json.Unmarshal(b, &pkg) == nil {
			for _, k := range []string{"check", "verify", "ci", "test"} {
				// jq's `.scripts[k] // empty`: null and false do not count.
				if v, ok := pkg.Scripts[k]; ok && string(v) != "null" && string(v) != "false" {
					if k == "test" {
						return "npm test"
					}
					return "npm run " + k
				}
			}
		}
	}
	for _, f := range []struct{ file, cmd string }{
		{"Cargo.toml", "cargo test"}, {"go.mod", "go test ./..."}, {"pyproject.toml", "pytest"},
	} {
		if isFile(filepath.Join(root, f.file)) {
			return f.cmd
		}
	}
	for _, f := range []string{"justfile", "Justfile", "Makefile", "makefile"} {
		for _, target := range []string{"check", "test"} {
			if hasTarget(filepath.Join(root, f), target) {
				if strings.EqualFold(f, "justfile") {
					return "just " + target
				}
				return "make " + target
			}
		}
	}
	return ""
}

func isFile(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Mode().IsRegular()
}

// hasTarget reports whether a line of the file starts with "target:".
func hasTarget(path, target string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if strings.HasPrefix(sc.Text(), target+":") {
			return true
		}
	}
	return false
}

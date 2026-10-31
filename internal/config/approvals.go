package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// CanonicalKey resolves path to an absolute, symlink-resolved form for use as
// an approvals-map key, matching how the trust gates and the CLIs key their own
// state. An approval made through one spelling of a path must hold for every
// spelling.
//
// It never errors: an unresolvable symlink falls back to the absolute path.
// This keys a user-choice map, so a best-effort key is safe.
func CanonicalKey(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return abs
	}
	return resolved
}

// KindApproved reports whether the user has accepted this kind for this
// repository. Approval is per repository on purpose: accepting droid for one
// project says nothing about another.
func (u *User) KindApproved(repo, kind string) bool {
	if u == nil {
		return false
	}
	repo = CanonicalKey(repo)
	for _, k := range u.Approvals[repo] {
		if k == kind {
			return true
		}
	}
	return false
}

// NeedsApproval reports whether r's kind is one the repository chose and the
// user has not approved for root. A kind from a flag, the environment or the
// user's own config is the user's decision and never needs approval.
func (u *User) NeedsApproval(root string, r Resolved) bool {
	return r.Kind.From == FromRepo && !u.KindApproved(root, r.Kind.Value)
}

// ApproveKind records an approval, preserving everything else in the file.
// It reads, merges and rewrites rather than serialising a fresh struct, so a
// key this version does not know about is not silently dropped.
func ApproveKind(configPath, repo, kind string) error {
	repo = CanonicalKey(repo)
	doc := map[string]any{}
	if body, err := os.ReadFile(configPath); err == nil && len(body) > 0 {
		if err := json.Unmarshal(body, &doc); err != nil {
			return err
		}
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}

	approvals, _ := doc["approvals"].(map[string]any)
	if approvals == nil {
		approvals = map[string]any{}
	}
	existing, _ := approvals[repo].([]any)
	for _, e := range existing {
		if s, ok := e.(string); ok && s == kind {
			return nil
		}
	}
	approvals[repo] = append(existing, kind)
	doc["approvals"] = approvals

	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		return err
	}
	body, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	tmp := configPath + ".tandem.tmp"
	if err := os.WriteFile(tmp, append(body, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, configPath); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

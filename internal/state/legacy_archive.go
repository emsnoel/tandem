package state

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/emsnoel/tandem/internal/build"
	"github.com/emsnoel/tandem/internal/workflow"
)

// verifiedArchive is a verified, parsed archive.
type verifiedArchive struct {
	dir      string
	manifest build.Manifest
	build    *build.Build
	digest   string
}

// readArchive verifies the archive and computes its digest. The digest covers
// content, never names or locations: the hash of every file except the
// path-bearing build record, plus that record with each file reference replaced
// by the hash of the file it names.
func readArchive(dir string) (*verifiedArchive, error) {
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", dir)
	}
	if err := build.VerifyArchive(dir); err != nil {
		return nil, fmt.Errorf("the archive does not verify: %w", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return nil, err
	}
	a := &verifiedArchive{dir: dir}
	if err := json.Unmarshal(raw, &a.manifest); err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	for _, f := range a.manifest.Files {
		fi, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(f.Path)))
		if err != nil {
			return nil, err
		}
		if !fi.Mode().IsRegular() {
			return nil, fmt.Errorf("%w: archived %s is a symlink or not a regular file", workflow.ErrCorruptArtifact, f.Path)
		}
	}
	if a.build, err = build.Load(filepath.Join(dir, "build")); err != nil {
		return nil, err
	}
	if a.build == nil {
		return nil, fmt.Errorf("%s has no archived build record", dir)
	}
	a.digest = a.computeDigest()
	return a, nil
}

// fileFor maps a path the build record holds to the manifest file it names,
// by the longest archive-relative suffix, so a copied or re-archived directory
// resolves the same way. "" when the record names nothing that was archived.
func (a *verifiedArchive) fileFor(ref string) string {
	best := ""
	for _, f := range a.manifest.Files {
		if strings.HasSuffix(ref, "/"+f.Path) && len(f.Path) > len(best) {
			best = f.Path
		}
	}
	return best
}

func (a *verifiedArchive) hashOf(rel string) string {
	for _, f := range a.manifest.Files {
		if f.Path == rel {
			return f.SHA256
		}
	}
	return ""
}

func (a *verifiedArchive) computeDigest() string {
	h := sha256.New()
	var lines []string
	for _, f := range a.manifest.Files {
		if f.Path == "build/build.json" {
			continue
		}
		lines = append(lines, fmt.Sprintf("%s\t%s\t%d", f.Path, f.SHA256, f.Size))
	}
	sort.Strings(lines)
	for _, l := range lines {
		fmt.Fprintln(h, l)
	}
	norm := *a.build
	norm.Tasks = append([]build.Task(nil), a.build.Tasks...)
	for i := range norm.Tasks {
		t := &norm.Tasks[i]
		for _, ref := range []*string{&t.Brief, &t.CheckLog, &t.Report, &t.ReviewReport} {
			if *ref != "" {
				*ref = "sha256:" + a.hashOf(a.fileFor(*ref))
			}
		}
	}
	record, _ := json.Marshal(&norm)
	fmt.Fprintf(h, "record\t%s\n", record)
	return hex.EncodeToString(h.Sum(nil))
}

// provenance describes the import: what the legacy record said, where each
// piece of evidence is stored, and what was never recorded.
func (a *verifiedArchive) provenance(files map[string]string) (map[string]any, []taskBlobs) {
	ref := func(path string) any {
		if path == "" {
			return nil
		}
		if h := files[a.fileFor(path)]; h != "" {
			return h
		}
		return nil
	}
	var tasks []map[string]any
	var blobs []taskBlobs
	for i, t := range a.build.Tasks {
		entry := map[string]any{
			"id": fmt.Sprintf("task-%d", i+1), "ordinal": i + 1, "title": t.Title,
			"legacy_status": t.Status, "check_exit": t.CheckExit, "usage": t.Usage,
			"files": t.Files, "accepted": t.Accepted, "accepted_from": t.AcceptedFrom,
			"evidence": map[string]any{
				"brief": ref(t.Brief), "check_log": ref(t.CheckLog), "report": ref(t.Report), "review_report": ref(t.ReviewReport),
			},
			"unknown": unverifiable,
		}
		if t.Review != nil {
			// verdict is the triage judgment ("review", "clean" or
			// "unavailable"); result is the reviewer pane's own verdict.
			entry["legacy_review"] = map[string]any{"verdict": t.Review.Verdict, "result": t.Review.Result, "rounds": t.ReviewRounds,
				"skipped": t.Review.Skipped, "reviewed": t.Review.Reviewed, "changed": t.Review.Changed, "delivered": t.Review.Delivered}
		}
		tasks = append(tasks, entry)
		brief, _ := ref(t.Brief).(string)
		blobs = append(blobs, taskBlobs{brief: brief})
	}
	return map[string]any{
		"schema": "legacy-import/v1", "build_id": a.build.ID, "title": a.build.Title,
		"plan_path": a.build.Plan, "plan_sha256": a.build.PlanSHA, "builder": a.build.Builder, "reviewer": a.build.Reviewer,
		"planner": a.build.Planner, "check_command": a.build.Check, "judge": a.build.Judge,
		"task_input_budget": a.build.Budget, "created": a.build.Created, "legacy_state": a.build.State,
		"stop_reason": a.build.StopReason, "stop_task": a.build.StopTask,
		"archive_digest": a.digest, "unknown": unverifiable, "tasks": tasks,
	}, blobs
}

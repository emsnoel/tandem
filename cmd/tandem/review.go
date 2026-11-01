package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/emsnoel/tandem/internal/config"
	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/gate"
	"github.com/emsnoel/tandem/internal/paths"
)

// reviewMeta is the sidecar `tandem gate record` writes beside each review for
// bin/tandem-verdict. Its keys and their order are the ones the script wrote
// with jq before, so older records read the same.
type reviewMeta struct {
	Plan     string `json:"plan"`
	Verdict  string `json:"verdict"`
	Kind     string `json:"kind"`
	Autonomy string `json:"autonomy"`

	// ReviewedBy is the pane role, "reviewer" for current records. An older record
	// without it is read as a builder review.
	ReviewedBy            string `json:"reviewed_by"`
	ReviewerKind          string `json:"reviewer_kind"`
	ReviewerModel         string `json:"reviewer_model"`
	ReviewerModelMeasured bool   `json:"reviewer_model_measured"`

	// OverrideReason is written by bin/tandem-handoff for --because, so one ruling
	// stands until the plan changes.
	OverrideReason string `json:"override_reason"`
}

// builderRecord is what bin/tandem-pair writes. It is the single source for
// who the builder is; nothing re-resolves it.
type builderRecord struct {
	Kind     string `json:"kind"`
	Autonomy string `json:"autonomy"`
}

// findReview returns the newest review for planAbs, and its id. Ids are
// timestamp-prefixed to the second; ties within a second are harmless because
// Review re-checks every binding afterwards.
func findReview(reviewsDir, planAbs string) (id string, meta reviewMeta, found bool) {
	entries, err := os.ReadDir(reviewsDir)
	if err != nil {
		// A missing directory means no review was ever made: absent, not an error.
		return "", reviewMeta{}, false
	}
	var ids []string
	metas := map[string]reviewMeta{}
	// unreadableIDs holds every sidecar that could not be parsed, whatever plan it
	// names.
	var unreadableIDs []string
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		thisID := strings.TrimSuffix(e.Name(), ".json")
		b, err := os.ReadFile(filepath.Join(reviewsDir, e.Name()))
		if err != nil {
			unreadableIDs = append(unreadableIDs, thisID)
			continue
		}
		var m reviewMeta
		if err := json.Unmarshal(b, &m); err != nil {
			// Skip a malformed sidecar but remember its id: it might be a newer review of
			// planAbs.
			unreadableIDs = append(unreadableIDs, thisID)
			continue
		}
		if m.Plan != planAbs {
			continue
		}
		ids = append(ids, thisID)
		metas[thisID] = m
	}
	if len(ids) == 0 {
		return "", reviewMeta{}, false
	}
	sort.Strings(ids)
	newest := ids[len(ids)-1]
	// Fail closed: an unreadable sidecar newer than the newest readable one may
	// have superseded it, so report absent.
	for _, u := range unreadableIDs {
		if u > newest {
			return "", reviewMeta{}, false
		}
	}
	return newest, metas[newest], true
}

func cmdGateReview(args []string) int {
	fs := flag.NewFlagSet("gate review", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	plan := fs.String("plan", "", "absolute path to the plan being handed over")
	root := fs.String("root", "", "repository root")
	expectReview := fs.String("expect-review", "", "the review id the caller believes is newest")
	if err := fs.Parse(args); err != nil {
		return exitcode.Usage
	}
	if *plan == "" || *root == "" {
		fmt.Fprintln(os.Stderr, "usage: tandem gate review --plan PATH --root DIR [--expect-review ID]")
		return exitcode.Usage
	}

	in, id := reviewInputFor(*plan, *root)
	in.ExpectedID = *expectReview

	v := gate.Review(in)
	emit(v)
	// The review id is always the last line when there is one. On a refusal it
	// follows the reason and the compared sides; on a pass it is all of stdout.
	// bin/tandem-handoff uses it to find a stored ruling.
	if in.Found {
		fmt.Println(id)
	}
	return v.Code
}

// reviewInputFor gathers everything gate.Review and gate.ReviewScope judge for
// planAbs. It is the only place those files are read, so the two cannot
// disagree about which review counts.
func reviewInputFor(planAbs, root string) (gate.ReviewInput, string) {
	id, meta, found := findReview(filepath.Join(paths.Relay(root), "reviews"), planAbs)

	// IdentityMeasured stays false when the record is missing or incomplete,
	// so an unreadable identity refuses instead of comparing "" to "".
	var kindNow, autonomyNow string
	identityMeasured := false
	if b, err := os.ReadFile(filepath.Join(paths.Relay(root), "builder.json")); err == nil {
		var rec builderRecord
		if json.Unmarshal(b, &rec) == nil && rec.Kind != "" && rec.Autonomy != "" {
			kindNow, autonomyNow, identityMeasured = rec.Kind, rec.Autonomy, true
		}
	}

	// ReviewerRecorded stays false when reviewer.json is missing or
	// incomplete: there is then no reviewer to bind a review to.
	rrec, rerr := config.LoadReviewer(paths.Relay(root))
	reviewerRecorded := rerr == nil

	in := gate.ReviewInput{
		ReviewerRecorded:              reviewerRecorded,
		ReviewerKindNow:               rrec.Kind,
		ReviewerKindReviewed:          meta.ReviewerKind,
		ReviewerModelNow:              rrec.Model,
		ReviewerModelReviewed:         meta.ReviewerModel,
		ReviewerModelMeasuredNow:      rrec.ModelMeasured,
		ReviewerModelMeasuredReviewed: meta.ReviewerModelMeasured,
		ReviewedBy:                    meta.ReviewedBy,
		Found:                         found,
		KindNow:                       kindNow,
		KindReviewed:                  meta.Kind,
		AutonomyNow:                   autonomyNow,
		AutonomyReviewed:              meta.Autonomy,
		IdentityMeasured:              identityMeasured,
		Verdict:                       meta.Verdict,
		OverrideReason:                meta.OverrideReason,
		NewestID:                      id,
	}

	// Read both plan texts. PlanMeasured is true only when BOTH reads
	// succeeded, so an unreadable file cannot compare equal to another.
	if found {
		now, errNow := os.ReadFile(planAbs)
		was, errWas := os.ReadFile(filepath.Join(paths.Relay(root), "reviews", id+".plan.md"))
		if errNow == nil && errWas == nil {
			in.PlanNow, in.PlanReviewed, in.PlanMeasured = string(now), string(was), true
		}
	}
	return in, id
}

// cmdGateScope prints how much of a plan the next review must read: "full",
// or "diff" and, on the next line, the review whose snapshot the change is
// measured against (gate.ReviewScope). It never refuses; exit 0 unless misused.
func cmdGateScope(args []string) int {
	fs := flag.NewFlagSet("gate scope", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	plan := fs.String("plan", "", "absolute path to the plan about to be reviewed")
	root := fs.String("root", "", "repository root")
	if err := fs.Parse(args); err != nil {
		return exitcode.Usage
	}
	if *plan == "" || *root == "" {
		fmt.Fprintln(os.Stderr, "usage: tandem gate scope --plan PATH --root DIR")
		return exitcode.Usage
	}
	in, id := reviewInputFor(*plan, *root)
	mode := gate.ReviewScope(in)
	fmt.Println(mode)
	if mode == "diff" {
		fmt.Println(id)
	}
	return exitcode.OK
}

// latestReviewedPlan returns the plan named by the newest review sidecar whose
// plan file still exists. Unreadable sidecars are skipped: this only picks a
// default.
func latestReviewedPlan(reviewsDir string) (string, bool) {
	entries, err := os.ReadDir(reviewsDir)
	if err != nil {
		return "", false
	}
	var ids []string
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".json" {
			ids = append(ids, strings.TrimSuffix(e.Name(), ".json"))
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(ids)))
	for _, id := range ids {
		b, err := os.ReadFile(filepath.Join(reviewsDir, id+".json"))
		if err != nil {
			continue
		}
		var m reviewMeta
		if json.Unmarshal(b, &m) != nil || m.Plan == "" {
			continue
		}
		if st, err := os.Stat(m.Plan); err == nil && st.Mode().IsRegular() {
			return m.Plan, true
		}
	}
	return "", false
}

// cmdGateLatestPlan prints the most recently reviewed plan, or nothing when
// there is none. Nothing is not an error: callers then fall back to the newest
// file in the plans directory.
func cmdGateLatestPlan(args []string) int {
	fs := flag.NewFlagSet("gate latest-plan", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	root := fs.String("root", "", "repository root")
	if err := fs.Parse(args); err != nil {
		return exitcode.Usage
	}
	if *root == "" {
		fmt.Fprintln(os.Stderr, "usage: tandem gate latest-plan --root DIR")
		return exitcode.Usage
	}
	if plan, ok := latestReviewedPlan(filepath.Join(paths.Relay(*root), "reviews")); ok {
		fmt.Println(plan)
	}
	return exitcode.OK
}

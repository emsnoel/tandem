package controller

import (
	"context"
	"encoding/json"
	"errors"
	"hash/crc32"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/evalrow"
	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

type offlineDispatch struct {
	Count           int `json:"count"`
	Bytes           int `json:"bytes"`
	NormalizedBytes int `json:"normalized_bytes"`
}

type offlineWorkflowRow struct {
	evalrow.Row
	ReviewMode          string                     `json:"review_mode"`
	Dispatches          map[string]offlineDispatch `json:"dispatches"`
	ArtifactReads       int                        `json:"artifact_reads"`
	DeniedArtifactReads int                        `json:"denied_artifact_reads"`
	ArtifactBytes       int                        `json:"artifact_bytes"`
	Repairs             int                        `json:"repairs"`
	Admissions          int                        `json:"admissions"`
	DuplicateAdmissions int                        `json:"duplicate_admissions"`
	Handbacks           int                        `json:"handbacks"`
}

// Run this fixture unchanged on the candidate and on a git archive of 711ed6d.
// Copy only this file, review_handback_e2e_test.go (the CLI setup helper) and
// tests/test-review-routing.sh to the archive. Set TANDEM_WORKFLOW_EVAL_ARM
// to the revision and run this test verbosely and test-review-routing.
func TestOfflineWorkflowComparison(t *testing.T) {
	arm := os.Getenv("TANDEM_WORKFLOW_EVAL_ARM")
	if arm == "" {
		t.Skip("set TANDEM_WORKFLOW_EVAL_ARM to candidate or 711ed6d")
	}
	if arm != "candidate" && arm != "711ed6d" {
		t.Fatalf("unknown workflow comparison arm %q", arm)
	}
	bin := handbackTestBin(t)
	t.Run("invalid-plan", func(t *testing.T) {
		e := newEvalEnv(t, evalrow.Scenario{ID: "invalid-plan", Tasks: 1})
		e.setPlan("# plan\n### Task 1: one\nTask-ID: one\nTask-ID: two\nstep\n")
		if err := e.begin(); err == nil || !strings.Contains(err.Error(), "Task-ID is given twice") || e.runID != "" || e.sent() != 0 {
			t.Fatalf("invalid plan unexpectedly started a run: %q, %v", e.runID, err)
		}
		m := offlineWorkflowRow{Row: evalrow.Row{
			SchemaVersion: evalrow.SchemaVersion, Arm: arm, PairID: "workflow-invalid-plan", Scenario: "invalid-plan",
			Mode: "fixture", State: "rejected", UsageCompleteness: workflow.UsageUnavailable, Reason: "no run was created",
		}}
		logOfflineWorkflowRow(t, m)
	})
	for _, mode := range []string{"manual", "direct"} {
		for _, name := range []string{"clean", "repair", "empty-patch", "restart"} {
			t.Run(mode+"/"+name, func(t *testing.T) {
				compareOfflineWorkflow(t, bin, arm, mode, name)
			})
		}
	}
}

func compareOfflineWorkflow(t *testing.T, bin, arm, mode, name string) {
	t.Helper()
	sc := evalrow.Scenario{ID: name, Tasks: 1, Shape: "chain"}
	e := newEvalEnv(t, sc)
	pair := "workflow-" + mode + "-" + name
	e.ids = int(crc32.ChecksumIEEE([]byte(pair))%100000) * 1000
	e.setPlan("# Eval plan\n\n## Global Constraints\n\n- Keep other files unchanged.\n\n" +
		"### Task 1: Greeting\nTask-ID: t1\n\nWrite the greeting to work1.txt.\n")
	if name == "empty-patch" {
		write(t, e.root, "work1.txt", "Hello.\n")
		git(t, e.root, "add", "work1.txt")
		git(t, e.root, "commit", "-qm", "preexisting greeting")
	}
	// The log exceeds the old excerpt bound and fits in a single artifact read.
	log := "\n== shell syntax ==\n" + strings.Repeat("  ok   the routine check succeeded\n", 140) + "\nall checks passed\n"
	e.c.d.CheckExecutor = func(ctx context.Context, req CheckRequest) (CheckResult, error) {
		res, err := e.chk.run(ctx, req)
		if err == nil {
			res.LogHash, err = e.art.Put(ctx, strings.NewReader(log))
		}
		return res, err
	}
	policy, err := e.c.put(ctx, `{"enforce_budgets":true}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.CreateCollaboration(ctx, state.CollaborationRequest{ID: testCollab, PolicyHash: policy,
		EnforceBudgets: true, Unlimited: []string{"codex", "claude"}}); err != nil {
		t.Fatal(err)
	}
	e.registerPlanner(testCollab)
	req := e.startRequest()
	req.Collaboration, req.CheckCommand = testCollab, "./tests/run.sh"
	if mode == "direct" {
		req.ReviewProtocol, req.ReviewDelivery, req.AcceptancePolicyPath = 3, "mailbox", e.policyPath()
	}
	e.startWith(req)
	m := offlineWorkflowRow{ReviewMode: mode, Dispatches: map[string]offlineDispatch{
		"builder": {}, "reviewer": {}, "planner": {},
	}}
	if name == "restart" {
		e.crashAfterFirstPrompt()
	} else {
		n := 1
		if name == "repair" {
			n = 2
		}
		report := filepath.Join(t.TempDir(), "report.md")
		write(t, filepath.Dir(report), filepath.Base(report), "Build fixture completed.\n")
		for i := 0; i < n; i++ {
			e.tick()
			att := e.current("t1")
			prompt := e.promptText(workflow.EffectBuilderDispatch, att)
			ack := regexp.MustCompile(`(?m)^    (.+) --started$`).FindStringSubmatch(prompt)
			submit := regexp.MustCompile(`(?m)^    (.+) --report <file>$`).FindStringSubmatch(prompt)
			if ack == nil || submit == nil || ack[1] != submit[1] {
				t.Fatal("builder prompt lacks paired handback commands")
			}
			if helper := !strings.HasPrefix(ack[1], "tandem-handback "); helper != (arm == "candidate") {
				t.Fatalf("unexpected builder command for %s: %s", arm, ack[1])
			}
			m.handback(t, bin, e.root, ack[1]+" --started")
			e.tick()
			m.handback(t, bin, e.root, ack[1]+" --started")
			e.tick()
			if got := e.eventCount(workflow.EventBuilderStarted); got != i+1 {
				t.Fatalf("duplicate acknowledgement admitted: %d starts for %d attempts", got, i+1)
			}
			if name != "empty-patch" {
				value := "Hello.\n"
				if n == 2 && i == 0 {
					value = "Goodbye.\n"
				}
				write(t, e.root, "work1.txt", value)
			}
			e.settle(att)
			m.handback(t, bin, e.root, submit[1]+" --report "+shellQuote(report))
			e.tick()
			if got := e.eventCount(workflow.EventBuilderSubmitted); got != i+1 {
				t.Fatalf("handback admitted %d submissions for %d attempts", got, i+1)
			}
			reviewPrompt := e.promptText(effectReviewPrompt, att)
			m.readReviewArtifacts(t, e, reviewPrompt, log)
			if mode == "manual" {
				for _, required := range []string{"Write the greeting to work1.txt.", "Keep other files unchanged."} {
					if strings.Contains(reviewPrompt, required) != (arm == "candidate") {
						t.Errorf("manual review requirement %q differs from %s expectation", required, arm)
					}
				}
			}
			if name == "empty-patch" && !strings.Contains(strings.ToLower(reviewPrompt), "working tree") {
				t.Fatal("empty patch review omitted the working tree instruction")
			}
			status := workflow.ReviewStatusPassed
			var findings []workflow.Finding
			if n == 2 && i == 0 {
				status = workflow.ReviewStatusChangesRequested
				findings = []workflow.Finding{{Severity: "high", Path: "work1.txt", Disposition: "open", Message: "Goodbye is not the requested greeting."}}
			}
			if mode == "direct" {
				p := e.reviewPromptOf(att)
				res := e.resultFor(att, status, findings...)
				e.sendReview(att, p, EventReviewResult, string(encode(res)))
				e.reviewerFinished(att, "reviewer-conversation")
			} else if err := e.review(att, status, findings...); err != nil {
				t.Fatal(err)
			}
			e.tick()
		}
	}
	m.Row = e.row(arm, pair)
	m.SchemaVersion = evalrow.SchemaVersion
	m.Reason = "scripted fixture with no provider logs"
	m.SourceManifest = ""
	for _, d := range e.tr.sends {
		role := "builder"
		switch d.Kind {
		case effectReviewPrompt:
			role = "reviewer"
		case effectNotify, effectVerifyPrompt, effectRecoveryDecision:
			role = "planner"
		}
		raw, err := e.art.ReadAll(ctx, d.PromptHash, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		detail := m.Dispatches[role]
		detail.Count++
		detail.Bytes += len(raw)
		detail.NormalizedBytes += len(strings.ReplaceAll(string(raw), e.root, "/repo"))
		m.Dispatches[role] = detail
	}
	attempts, err := e.st.TaskAttempts(ctx, e.runID, "t1")
	if err != nil {
		t.Fatal(err)
	}
	m.Repairs = len(attempts) - 1
	m.Admissions = e.countWhere("SELECT count(*) FROM dispatch_admissions")
	m.DuplicateAdmissions = m.Admissions - e.countWhere("SELECT count(DISTINCT effect_id) FROM dispatch_admissions")
	if m.Admissions != m.Dispatches["builder"].Count+m.Dispatches["reviewer"].Count ||
		m.DuplicateAdmissions != 0 || m.PendingTasks != 0 || m.UsageCompleteness != workflow.UsageUnavailable {
		t.Errorf("unexpected admission, pending work or usage in offline row: %+v", m)
	}
	if name == "restart" {
		if m.State != "paused" || m.PauseCode != "writer_unknown" || m.FailedTasks != 1 || m.AcceptedTasks != 0 ||
			m.Dispatches["builder"].Count != 1 || m.Dispatches["reviewer"].Count != 0 || m.Dispatches["planner"].Count != 0 {
			t.Errorf("unexpected restart: %+v", m)
		}
	} else {
		if m.State != "finished" || m.AcceptedTasks != 1 || m.FailedTasks != 0 || e.acceptEvents() != 1 ||
			m.Dispatches["builder"].Count != len(attempts) || m.Dispatches["reviewer"].Count != len(attempts) || m.Dispatches["planner"].Count != 1 {
			t.Errorf("unexpected delivery or acceptance: %+v", m)
		}
		if mode == "direct" && (m.ArtifactReads != len(attempts) || (m.DeniedArtifactReads == 0) != (arm == "candidate")) {
			t.Errorf("unexpected check log reads: %+v", m)
		}
	}
	logOfflineWorkflowRow(t, m)
}

func logOfflineWorkflowRow(t *testing.T, m offlineWorkflowRow) {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("WORKFLOW_EVAL %s", raw)
}

func (m *offlineWorkflowRow) handback(t *testing.T, bin, root, command string) {
	t.Helper()
	cmd := exec.Command("bash", "-c", command)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("handback: %v\n%s", err, out)
	}
	m.Handbacks++
}

func (m *offlineWorkflowRow) readReviewArtifacts(t *testing.T, e *evalEnv, prompt, log string) {
	t.Helper()
	re := regexp.MustCompile(`tandem artifact read ([0-9a-f]{64}) --attempt ([0-9a-f]{32})`)
	for _, match := range re.FindAllStringSubmatch(prompt, -1) {
		m.ArtifactReads++
		got, err := ReadArtifact(ctx, e.st, e.art, e.runID, match[2], ArtifactRequest{Hash: match[1]})
		if errors.Is(err, ErrArtifactDenied) {
			m.DeniedArtifactReads++
			continue
		}
		if err != nil || got.Role != "check log" || got.Text != log || got.NextOffset != nil {
			t.Fatalf("read returned %d of %d bytes for %q: %v", len(got.Text), len(log), got.Role, err)
		}
		m.ArtifactBytes += len(got.Text)
	}
}

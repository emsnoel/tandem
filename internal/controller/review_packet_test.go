package controller

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

// fixturePacket is a packet with every part filled in, for rendering tests.
func fixturePacket(stage string) ReviewPacket {
	return ReviewPacket{Stage: stage, RunID: hex32(1), AttemptID: hex32(2), JobID: hex32(3), EffectID: hex32(4), Nonce: hex32(5),
		TaskOrdinal: 2, TaskTotal: 3, TaskSlug: "greeting", HandbackDir: "/repo/.tandem-relay/durable/run/review/effect",
		Brief:        "Add a greeting to the CLI.\nKeep it to one line.",
		Constraints:  "Do not touch the config loader.",
		Dependencies: []DependencyEvidence{{TaskID: "config", AttemptID: hex32(6), PatchHash: strings.Repeat("a", 64), TreeHash: strings.Repeat("b", 64)}},
		Check: CheckEvidence{Command: "go test ./...", Ran: true, ExitCode: 0, Complete: true, CertificateID: hex32(7), JobID: hex32(8),
			LogHash: strings.Repeat("c", 64), LogExcerpt: "ok  \texample/greet\t0.012s\n"},
		PatchHash: strings.Repeat("d", 64), TreeHash: strings.Repeat("e", 64),
		Patch: "--- a/greet.go\n+++ b/greet.go\n@@ -1 +1,3 @@\n+func Greet() string { return \"hello\" }\n"}
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "review-packet", name+".golden")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (UPDATE_GOLDEN=1 writes it)", err)
	}
	if string(want) != got {
		t.Errorf("%s differs from its golden file:\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

func TestReviewPacketGolden(t *testing.T) {
	golden(t, "builder-baseline", renderReviewPacket(fixturePacket(stageReview), 2))
	golden(t, "builder-handback", renderReviewPacket(fixturePacket(stageReview), 3))
	golden(t, "verification-handback", renderReviewPacket(fixturePacket(stageIndependentVerification), 3))
}

func TestReviewPacketCarriesEveryMandatoryField(t *testing.T) {
	for _, stage := range []string{stageReview, stageIndependentVerification} {
		p := fixturePacket(stage)
		text := renderReviewPacket(p, 3)
		for name, want := range map[string]string{
			"the exact brief":        p.Brief,
			"the exact constraints":  p.Constraints,
			"a dependency's task":    p.Dependencies[0].TaskID,
			"its accepted patch":     p.Dependencies[0].PatchHash,
			"its accepted tree":      p.Dependencies[0].TreeHash,
			"the check command":      "command: go test ./...",
			"the check exit code":    "exit code: 0",
			"the check completeness": "complete: true",
			"the check certificate":  p.Check.CertificateID,
			"the check log":          "example/greet",
			"the patch":              p.Patch,
			"the patch hash":         "patch: " + p.PatchHash,
			"the tree hash":          "tree: " + p.TreeHash,
			"the run":                "run: " + p.RunID,
			"the attempt":            "attempt: " + p.AttemptID,
			"the job":                "job: " + p.JobID,
			"the verdict file":       p.HandbackDir + "/verdict.json",
			"the handback command":   p.HandbackDir + "/handback",
		} {
			if !strings.Contains(text, want) {
				t.Errorf("%s: the packet leaves out %s (%q)", stage, name, want)
			}
		}
	}
	// A run with no declared check says so rather than leave the section out.
	p := fixturePacket(stageReview)
	p.Check, p.Dependencies = CheckEvidence{}, nil
	text := renderReviewPacket(p, 3)
	if !strings.Contains(text, "the run declares no check") || !strings.Contains(text, "== Dependencies\nnone") {
		t.Errorf("an absent check or dependency is not reported:\n%s", text)
	}
}

// The body is the same under both protocols. Only the delivery block, how the
// result gets back, is the treatment.
func TestReviewPacketTreatmentChangesOnlyTheDeliveryBlock(t *testing.T) {
	for _, stage := range []string{stageReview, stageIndependentVerification} {
		base := renderReviewPacket(fixturePacket(stage), 2)
		treat := renderReviewPacket(fixturePacket(stage), 3)
		const marker = "\n== Result\n"
		bHead, bTail, ok1 := strings.Cut(base, marker)
		tHead, tTail, ok2 := strings.Cut(treat, marker)
		if !ok1 || !ok2 {
			t.Fatalf("%s: a packet has no delivery block", stage)
		}
		if bHead != tHead {
			t.Errorf("%s: the evidence differs between the baseline and handback protocols", stage)
		}
		if bTail == tTail {
			t.Errorf("%s: the delivery block does not differ between protocols", stage)
		}
		if strings.Contains(bTail, "tandem-handback") || !strings.Contains(tTail, "/handback") ||
			!strings.Contains(tTail, "--started") {
			t.Errorf("%s: protocol text is in the wrong packet:\nbaseline: %q\nhandback: %q", stage, bTail, tTail)
		}
	}
}

func TestReviewPacketTruncationIsExplicit(t *testing.T) {
	p := fixturePacket(stageIndependentVerification)
	whole := renderReviewPacket(p, 3)
	if strings.Contains(whole, "truncated") {
		t.Fatalf("a packet that cut nothing claims it did:\n%s", whole)
	}
	p.PatchTruncated, p.Check.LogTruncated = true, true
	cut := renderReviewPacket(p, 3)
	for name, want := range map[string]string{
		"the patch is marked":    "[patch truncated",
		"its artifact is named":  "artifact " + p.PatchHash,
		"its read command":       "tandem artifact read " + p.PatchHash + " --attempt " + p.AttemptID,
		"the log is marked":      "[log truncated",
		"the log's artifact":     "artifact " + p.Check.LogHash,
		"the log's read command": "tandem artifact read " + p.Check.LogHash + " --attempt " + p.AttemptID,
	} {
		if !strings.Contains(cut, want) {
			t.Errorf("%s: %q missing from\n%s", name, want, cut)
		}
	}
}

// reviewPromptOf returns the text of a run's review prompt for an attempt.
func (e *cenv) promptText(kind string, att workflow.Attempt) string {
	e.t.Helper()
	for _, p := range e.effects(kind) {
		if p.AttemptID == att.ID {
			raw, err := e.art.ReadAll(ctx, p.PayloadHash, 1<<20)
			if err != nil {
				e.t.Fatal(err)
			}
			return string(raw)
		}
	}
	e.t.Fatalf("attempt %s has no %s", att.ID, kind)
	return ""
}

func TestReviewPacketBuiltFromARealRun(t *testing.T) {
	e := newCEnv(t)
	e.startReviewHandback()
	att := e.toReview("task-1", 1)
	cur, _ := e.st.GetAttempt(ctx, att.ID)
	job, _ := e.jobByStage(att, state.StageBuilderReview)
	prompt := e.effects("review_prompt")[0]
	text := e.promptText("review_prompt", att)
	for name, want := range map[string]string{
		"the exact brief":     "Do the first thing.",
		"the task":            "task-1",
		"the check command":   "command: true",
		"the check result":    "exit code: 0",
		"the patch hash":      "patch: " + cur.PatchHash,
		"the tree hash":       "tree: " + cur.EndSnapshotHash,
		"the job":             "job: " + job.ID,
		"the effect handback": filepath.Join(RunDir(e.relay, e.runID), "review", prompt.ID, "handback"),
		"the changed file":    "work1.txt",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the packet leaves out %s (%q)", name, want)
		}
	}
	if !strings.Contains(text, "== Dependencies\nnone") {
		t.Errorf("the first task should report no dependencies:\n%s", text)
	}
	// The reply's identity is the one the controller registered.
	id, nonce, err := e.st.DerivedEffectIdentity(ctx, e.reviewDispatchOfAttempt(att).ID, effectReviewPrompt)
	if err != nil || id != prompt.ID || nonce != prompt.Nonce {
		t.Fatalf("the prompt named effect %s/%s, the stored one is %s/%s (%v)", id, nonce, prompt.ID, prompt.Nonce, err)
	}
}

func (e *cenv) reviewDispatchOfAttempt(att workflow.Attempt) workflow.Effect {
	e.t.Helper()
	for _, x := range e.effects(workflow.EffectReviewDispatch) {
		if x.AttemptID == att.ID {
			return x
		}
	}
	e.t.Fatalf("attempt %s has no review dispatch", att.ID)
	return workflow.Effect{}
}

func TestReviewPacketMarksARealTruncation(t *testing.T) {
	e := newCEnv(t)
	e.startReviewHandback()
	write(t, e.root, "big.txt", strings.Repeat("a line that makes the patch large\n", 2000))
	att := e.toReview("task-1", 1)
	text := e.promptText("review_prompt", att)
	cur, _ := e.st.GetAttempt(ctx, att.ID)
	if !strings.Contains(text, "[patch truncated") || !strings.Contains(text, "tandem artifact read "+cur.PatchHash+" --attempt "+att.ID) {
		t.Fatalf("a large patch was cut without saying how to read the rest:\n%.600s", text)
	}
}

// A run that did not opt in keeps the original prompt, byte for byte.
func TestReviewPacketLeavesTheLegacyPromptAloneWithoutOptIn(t *testing.T) {
	e := newCEnv(t)
	e.start()
	att := e.toReview("task-1", 1)
	text := e.promptText("review_prompt", att)
	if !strings.Contains(text, "Reply with ONE JSON object and nothing else:") || strings.Contains(text, "== Packet") {
		t.Fatalf("a run that did not opt in got the new packet:\n%.400s", text)
	}
}

// Production acceptance is decided from the run's own persisted evidence. The
// evaluation-only audit lives outside this repository and has no call into the controller, the
// workflow reducer or the store: it can neither repair nor waive a failed stage.
func TestAcceptanceNeverConsultsTheEvaluationAudit(t *testing.T) {
	forbidden := regexp.MustCompile(`evals/|audit_result|AuditResult|runAudit|evaluation_audit`)
	for _, dir := range []string{".", "../workflow", "../state"} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil || len(files) == 0 {
			t.Fatalf("no sources in %s: %v", dir, err)
		}
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			raw, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			if m := forbidden.Find(raw); m != nil {
				t.Errorf("%s refers to the evaluation audit (%q): production acceptance must not", f, m)
			}
		}
	}
}

// The review stage goes to the reviewer pane, never the builder, and the
// planner's packet leaves the reviewer pane's review out.
func TestPacketFramingNamesTheReviewerPane(t *testing.T) {
	r := renderReviewPacket(fixturePacket(stageReview), 3)
	if strings.Contains(r, "builder-side") || !strings.Contains(r, "You are the reviewer.") {
		t.Errorf("review packet framing:\n%s", r)
	}
	v := renderReviewPacket(fixturePacket(stageIndependentVerification), 3)
	if strings.Contains(v, "builder's review") || !strings.Contains(v, "The reviewer pane's review is not part of this packet") {
		t.Errorf("verification packet framing:\n%s", v)
	}
}

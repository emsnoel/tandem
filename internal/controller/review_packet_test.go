package controller

import (
	"context"
	"errors"
	"fmt"
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
	t.Parallel()
	golden(t, "builder-baseline", renderReviewPacket(fixturePacket(stageReview), 2))
	golden(t, "builder-handback", renderReviewPacket(fixturePacket(stageReview), 3))
	golden(t, "verification-handback", renderReviewPacket(fixturePacket(stageIndependentVerification), 3))
}

func TestReviewPacketCarriesEveryMandatoryField(t *testing.T) {
	t.Parallel()
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
			"the verdict file":       p.HandbackDir + "/verdict.txt",
			"the verdict as text":    "status: OUTCOME\n\nfinding: SEVERITY DISPOSITION PATH[:START[-END]]\n",
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

func TestReviewPacketTruncatedCheckLogCanBeRead(t *testing.T) {
	t.Parallel()
	e := newCEnv(t)
	const omitted = "the first line of the full log\n"
	log := omitted + strings.Repeat("check output\n", maxPacketLogBytes/len("check output\n")+1)
	e.c.d.CheckExecutor = func(ctx context.Context, req CheckRequest) (CheckResult, error) {
		res, err := e.chk.run(ctx, req)
		if err != nil {
			return res, err
		}
		res.LogHash, err = e.art.Put(ctx, strings.NewReader(log))
		return res, err
	}
	e.startReviewHandback()
	att := e.toReview("task-1", 1)
	packet := e.promptText("review_prompt", att)
	read := regexp.MustCompile(`(?m)^\[log truncated:[^\n]*tandem artifact read ([0-9a-f]{64}) --attempt ([0-9a-f]{32})\]$`).FindStringSubmatch(packet)
	if read == nil || read[2] != att.ID || strings.Contains(packet, omitted) {
		t.Fatal("the packet did not omit the first line and give an attempt-scoped read command")
	}
	got, err := ReadArtifact(ctx, e.st, e.art, e.runID, read[2], ArtifactRequest{Hash: read[1]})
	if err != nil {
		t.Fatal(err)
	}
	if got.Role != "check log" || got.Text != log || got.NextOffset != nil {
		t.Errorf("the command returned role %q and %d bytes, want the full %d-byte check log", got.Role, len(got.Text), len(log))
	}
}

func TestReviewPacketPreservesUnsuccessfulAndMissingCheckEvidence(t *testing.T) {
	t.Parallel()
	const command = "./tests/run.sh"
	log := "\n== shell syntax ==\n" + strings.Repeat("  ok   routine\n", 400) + "\nall checks passed\n"
	for _, tt := range []struct {
		name     string
		exit     int
		complete bool
	}{
		{name: "nonzero exit", exit: 1, complete: true},
		{name: "incomplete"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newCEnv(t)
			e.startReviewHandback(func(r *StartRequest) { r.CheckCommand = command })
			e.tick()
			att := e.current("task-1")
			if _, err := e.c.checkEvidence(ctx, runConfig{CheckCommand: command}, att); !errors.Is(err, workflow.ErrIncompleteEvidence) {
				t.Fatalf("declared check without a certificate: %v", err)
			}
			if check, err := e.c.checkEvidence(ctx, runConfig{}, att); err != nil || check.Ran {
				t.Fatalf("no declared check: %+v, %v", check, err)
			}
			// Failed and incomplete certificates cannot schedule a review. Persist
			// one and exercise the evidence reader and renderer directly.
			hash, err := e.c.put(ctx, log)
			if err != nil {
				t.Fatal(err)
			}
			inputs, err := e.c.put(ctx, "check inputs")
			if err != nil {
				t.Fatal(err)
			}
			job := workflow.Job{ID: hex32(10), AttemptID: att.ID, Kind: "check", InputHash: inputs, ConfigHash: inputs, RunnerKind: "local"}
			if err := e.st.InsertJob(ctx, job); err != nil {
				t.Fatal(err)
			}
			cert := state.CheckCertificate{ID: hex32(11), AttemptID: att.ID, JobID: job.ID, LogHash: hash,
				BeforeInputsHash: inputs, AfterInputsHash: inputs, CommandHash: inputs, EnvironmentHash: inputs, ExitCode: tt.exit, Complete: tt.complete}
			if err := e.st.RecordCheckCertificate(ctx, cert); err != nil {
				t.Fatal(err)
			}
			p := fixturePacket(stageReview)
			p.AttemptID = att.ID
			p.Check, err = e.c.checkEvidence(ctx, runConfig{CheckCommand: command}, att)
			if err != nil {
				t.Fatal(err)
			}
			packet := renderReviewPacket(p, 3)
			want := fmt.Sprintf("\n== Checks\ncommand: %s\nexit code: %d\ncomplete: %t\ncertificate: %s\n%s\n== Evidence under review\n",
				command, tt.exit, tt.complete, cert.ID, baselinePacketLog(log, hash, att.ID))
			if !strings.Contains(packet, want) {
				t.Fatal("the packet changed the baseline check evidence")
			}
			got, err := ReadArtifact(ctx, e.st, e.art, e.runID, att.ID, ArtifactRequest{Hash: hash})
			if err != nil || got.Role != "check log" || got.Text != log || got.NextOffset != nil {
				t.Fatalf("read returned %d of %d log bytes, err=%v", len(got.Text), len(log), err)
			}
			t.Logf("full packet bytes: baseline and candidate %d", len(packet))
		})
	}
}

// baselinePacketLog renders the check log as at 711ed6d.
func baselinePacketLog(log, hash, attempt string) string {
	const limit = 4 << 10
	if len(log) <= limit {
		return "log:\n" + log + "\n"
	}
	return fmt.Sprintf("log:\n%s\n[log truncated: only its last %d bytes are shown. The whole log is artifact %s: tandem artifact read %s --attempt %s]\n",
		log[len(log)-limit:], limit, hash, hash, attempt)
}

// A run that did not opt in keeps the original prompt, byte for byte.
func TestReviewPacketLeavesTheLegacyPromptAloneWithoutOptIn(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	r := renderReviewPacket(fixturePacket(stageReview), 3)
	if strings.Contains(r, "builder-side") || !strings.Contains(r, "You are the reviewer.") {
		t.Errorf("review packet framing:\n%s", r)
	}
	v := renderReviewPacket(fixturePacket(stageIndependentVerification), 3)
	if strings.Contains(v, "builder's review") || !strings.Contains(v, "The reviewer pane's review is not part of this packet") {
		t.Errorf("verification packet framing:\n%s", v)
	}
}

// toReviewUnchanged takes a task to review with the builder changing no file,
// as when the task's work is already in the tree from an earlier run.
func (e *cenv) toReviewUnchanged(taskID string) workflow.Attempt {
	e.t.Helper()
	e.tick()
	att := e.current(taskID)
	e.mustSend(taskID, workflow.EventBuilderStarted, "{}\n")
	e.settle(att)
	e.mustSend(taskID, workflow.EventBuilderSubmitted, "{\"message\":\"the work was already there; I changed nothing\"}\n")
	e.tick()
	return att
}

// An empty patch gives the reviewer nothing to judge, so the packet says the
// builder changed nothing and asks for the tree to be checked against the
// brief instead (issue #66).
func TestReviewPacketForAnEmptyPatchAsksForTheTree(t *testing.T) {
	t.Parallel()
	e := newCEnv(t)
	e.startReviewHandback()
	att := e.toReviewUnchanged("task-1")
	text := e.promptText("review_prompt", att)
	for _, want := range []string{
		"the builder changed no files on this attempt",
		"Check the working tree itself against the brief",
		"Pass if it already meets the brief",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("an empty patch's packet leaves out %q:\n%s", want, text)
		}
	}
}

func TestLegacyReviewPromptForAnEmptyPatchAsksForTheTree(t *testing.T) {
	t.Parallel()
	e := newCEnv(t)
	e.start()
	att := e.toReviewUnchanged("task-1")
	text := e.promptText("review_prompt", att)
	if !strings.Contains(text, "the builder changed no files on this attempt") || strings.HasSuffix(strings.TrimSpace(text), "The patch:") {
		t.Fatalf("a legacy prompt for an empty patch ends at an empty patch:\n%s", text)
	}
}

// The packet names the files the task lists, so the reviewer knows where to look.
func TestEmptyPatchNoteNamesTheTaskFiles(t *testing.T) {
	t.Parallel()
	p := fixturePacket(stageReview)
	p.Patch, p.TaskPaths = "", []string{"greet.go", "greet_test.go"}
	text := renderReviewPacket(p, 3)
	if !strings.Contains(text, "The task names these files:\n- greet.go\n- greet_test.go") {
		t.Fatalf("the note does not list the task's files:\n%s", text)
	}
}

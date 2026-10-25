package controller

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/emsnoel/tandem/internal/agent"
	"github.com/emsnoel/tandem/internal/runner"
	"github.com/emsnoel/tandem/internal/workflow"
)

// reviewerScript is a fixture CLI: it reads the review on stdin and prints a
// result bound to the job it names, with the status it was told to report.
func reviewerScript(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fixture-reviewer")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// passing reads the prompt's identity lines and answers with a passing review.
const passing = `in=$(cat | tr '\n' ' ')
f() { printf '%s' "$in" | sed -n "s/.*\"$1\":\"\([0-9a-f]*\)\".*/\1/p"; }
printf '{"schema_version":1,"job_id":"%s","attempt_id":"%s","patch_hash":"%s","tree_hash":"%s","status":"passed","findings":[]}\n' "$(f job_id)" "$(f attempt_id)" "$(f patch_hash)" "$(f tree_hash)"`

// directEnv is an environment where codex reviews run as direct processes through script.
func directEnv(t *testing.T, script string, launch ...string) *cenv {
	t.Helper()
	e := newCEnv(t)
	e.reviewer = runner.Reviewer{
		Entries: map[string]runner.DirectEntry{"codex": {
			Caps:     runner.ReviewCapabilities{Direct: true, StructuredResult: true, Cancellation: true},
			Versions: []string{"codex-fixture 1"},
			Evidence: "test fixture",
			Headless: func(l []string) []string { return append([]string{script}, l...) },
		}},
		Version: func(string) string { return "codex-fixture 1.4" },
		Grace:   200 * time.Millisecond,
	}
	// The builder is another kind, so a builder source read for the reviewer
	// would run the wrong CLI.
	e.builderKind, e.reviewerKind = "claude", "codex"
	e.reviewLaunch = func(string) ([]string, error) { return launch, nil }
	e.reviewTimeout = 20 * time.Second
	e.c = e.newController()
	return e
}

func TestDirectReviewUnsupportedFallsBackToAPaneJob(t *testing.T) {
	e := newCEnv(t)
	e.start()
	att := e.toReview("task-1", 1)
	jobs, _ := e.st.AttemptJobs(ctx, att.ID, "reviewer")
	if len(jobs) != 1 || jobs[0].RunnerKind != "pane-review" {
		t.Fatalf("with every capability off the review is a pane job: %+v", jobs)
	}
	if len(e.effects("review_prompt")) != 1 || e.delivered()["review_prompt"] != 1 {
		t.Errorf("the pane was not asked: %d prompts", e.delivered()["review_prompt"])
	}
	// An adapter that is only partly verified, or whose version does not match, is pane too.
	for name, mut := range map[string]func(*runner.Reviewer){
		"another version": func(r *runner.Reviewer) { r.Version = func(string) string { return "codex-fixture 2.0" } },
		"no version":      func(r *runner.Reviewer) { r.Version = nil },
		"no cancel": func(r *runner.Reviewer) {
			x := r.Entries["codex"]
			x.Caps.Cancellation = false
			r.Entries["codex"] = x
		},
	} {
		d := directEnv(t, "/bin/true")
		mut(&d.reviewer)
		d.c = d.newController()
		d.start()
		a := d.toReview("task-1", 1)
		if jobs, _ := d.st.AttemptJobs(ctx, a.ID, "reviewer"); len(jobs) != 1 || jobs[0].RunnerKind != "pane-review" {
			t.Errorf("%s: %+v", name, jobs)
		}
	}
}

func TestDirectReviewAcceptsAStructuredResult(t *testing.T) {
	e := directEnv(t, reviewerScript(t, passing))
	e.start()
	att := e.toReview("task-1", 1)
	jobs, _ := e.st.AttemptJobs(ctx, att.ID, "reviewer")
	if len(jobs) != 1 || jobs[0].RunnerKind != "direct-review" {
		t.Fatalf("%+v", jobs)
	}
	if got := e.reviewState(att); got != workflow.ReviewPassed {
		t.Fatalf("review is %s", got)
	}
	if len(e.effects("review_prompt")) != 0 {
		t.Error("a direct review also asked a pane")
	}
	var id struct{ Mode, Assurance, Note, Kind string }
	for h, role := range mustArtifacts(t, e, att) {
		if role == "review identity" {
			raw, _ := e.art.ReadAll(ctx, h, 1<<20)
			json.Unmarshal(raw, &id)
		}
	}
	if id.Mode != "direct" || id.Kind != "codex" || !strings.Contains(id.Note, "not provider-attested") || id.Assurance == "" {
		t.Errorf("identity %+v", id)
	}
	e.tick()
	if e.tasks()[0].AcceptedAttemptID != att.ID {
		t.Error("a passed direct review did not lead to acceptance")
	}
	// The process was run once, not again on every tick.
	if jobs, _ := e.st.AttemptJobs(ctx, att.ID, "reviewer"); len(jobs) != 1 {
		t.Errorf("%d reviewer jobs", len(jobs))
	}
}

func TestDirectReviewMalformedResultFails(t *testing.T) {
	for name, c := range map[string]struct{ body, want string }{
		"exit 0 with prose":   {`cat >/dev/null; echo "looks good to me"`, "not a valid review result"},
		"exit 0 with nothing": {`cat >/dev/null`, "not a valid review result"},
		"two objects":         {passing + "\n" + `printf '{}'`, "not a valid review result"},
		"exit 0 for the wrong job": {`cat >/dev/null; printf '{"schema_version":1,"job_id":"%s","attempt_id":"%s","patch_hash":"%s","tree_hash":"%s","status":"passed","findings":[]}' ` +
			strings.Repeat("9", 32) + ` ` + strings.Repeat("8", 32) + ` ` + strings.Repeat("a", 64) + ` ` + strings.Repeat("b", 64), "not a valid review result"},
		"a pass with an open high finding": {`cat >/dev/null; in=""` + "\n" + passing, ""},
		"nonzero exit":                     {`cat >/dev/null; echo "the model is unavailable" >&2; exit 3`, "exited 3"},
	} {
		body := c.body
		if name == "a pass with an open high finding" {
			body = `in=$(cat | tr '\n' ' ')
f() { printf '%s' "$in" | sed -n "s/.*\"$1\":\"\([0-9a-f]*\)\".*/\1/p"; }
printf '{"schema_version":1,"job_id":"%s","attempt_id":"%s","patch_hash":"%s","tree_hash":"%s","status":"passed","findings":[{"severity":"high","path":"a.go","disposition":"open","message":"bad"}]}' "$(f job_id)" "$(f attempt_id)" "$(f patch_hash)" "$(f tree_hash)"`
			c.want = "not a valid review result"
		}
		e := directEnv(t, reviewerScript(t, body))
		e.start()
		att := e.toReview("task-1", 1)
		if got := e.reviewState(att); got != workflow.ReviewFailed {
			t.Errorf("%s: review is %s", name, got)
		}
		if st, code := e.runState(); st != "paused" || code != "review_failed" {
			t.Errorf("%s: run is %s/%s", name, st, code)
		}
		e.tick()
		if e.tasks()[0].AcceptedAttemptID != "" {
			t.Errorf("%s: a failed review led to acceptance", name)
		}
		// The failure is on record and says why.
		h, found, _ := e.st.LatestReviewResult(ctx, att.ID)
		raw, _ := e.art.ReadAll(ctx, h, 1<<20)
		var res workflow.ReviewResult
		json.Unmarshal(raw, &res)
		if !found || res.Status != workflow.ReviewStatusFailed || len(res.Findings) != 1 || !strings.Contains(res.Findings[0].Message, c.want) {
			t.Errorf("%s: recorded %s", name, raw)
		}
	}
}

func TestDirectReviewTimeoutFailsTheReview(t *testing.T) {
	e := directEnv(t, reviewerScript(t, `cat >/dev/null; sleep 30`))
	e.reviewTimeout = 400 * time.Millisecond
	e.c = e.newController()
	e.start()
	att := e.toReview("task-1", 1)
	if got := e.reviewState(att); got != workflow.ReviewFailed {
		t.Fatalf("review is %s", got)
	}
	h, _, _ := e.st.LatestReviewResult(ctx, att.ID)
	raw, _ := e.art.ReadAll(ctx, h, 1<<20)
	if !strings.Contains(string(raw), "timed out") {
		t.Errorf("%s", raw)
	}
}

func TestDirectReviewPreservesPermissions(t *testing.T) {
	argfile := filepath.Join(t.TempDir(), "args")
	t.Setenv("ARGFILE", argfile)
	script := reviewerScript(t, `for a in "$@"; do printf '%s\n' "$a"; done > "$ARGFILE"
`+passing)
	// With no override, the launch arguments are what the adapter resolves for the
	// reviewer's recorded autonomy: the reviewer is no more permissive, and no less.
	for _, level := range []string{"readonly", "ask", "auto", "full"} {
		a, _ := agent.Get("codex")
		want, err := a.Resolve(agent.Spec{Kind: "codex", Role: agent.RoleReviewer, Autonomy: agent.Autonomy(level)})
		if err != nil {
			continue // a level this adapter does not offer
		}
		e := directEnv(t, script)
		e.reviewLaunch = nil
		e.c = e.newController()
		if err := os.WriteFile(filepath.Join(e.relay, "reviewer.json"), []byte(`{"kind":"codex","name":"reviewer-session","autonomy":"`+level+`"}`), 0o644); err != nil {
			os.MkdirAll(e.relay, 0o755)
			os.WriteFile(filepath.Join(e.relay, "reviewer.json"), []byte(`{"kind":"codex","name":"reviewer-session","autonomy":"`+level+`"}`), 0o644)
		}
		// The builder's own record disagrees; the reviewer never reads it.
		if err := os.WriteFile(filepath.Join(e.relay, "builder.json"), []byte(`{"kind":"codex","autonomy":"readonly"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		e.start()
		att := e.toReview("task-1", 1)
		if e.reviewState(att) != workflow.ReviewPassed {
			t.Fatalf("%s: review is %s", level, e.reviewState(att))
		}
		raw, _ := os.ReadFile(argfile)
		got := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
		if len(want.Args) > 0 && strings.Join(got, "\x00") != strings.Join(want.Args, "\x00") {
			t.Errorf("%s: resolved %q, the reviewer received %q", level, want.Args, got)
		}
	}
	// Without a recorded autonomy the settings cannot be known, so nothing runs direct.
	e := directEnv(t, script)
	e.reviewLaunch = nil
	e.c = e.newController()
	e.start()
	att := e.toReview("task-1", 1)
	if jobs, _ := e.st.AttemptJobs(ctx, att.ID, "reviewer"); len(jobs) != 1 || jobs[0].RunnerKind != "pane-review" {
		t.Errorf("a direct review was started without the builder's settings: %+v", jobs)
	}
}

func TestDirectReviewCancellationStopsChildren(t *testing.T) {
	pidfile := filepath.Join(t.TempDir(), "pid")
	t.Setenv("PIDFILE", pidfile)
	e := directEnv(t, reviewerScript(t, `cat >/dev/null
sleep 300 &
echo $! > "$PIDFILE"
wait`))
	e.start()
	e.tick()
	att := e.current("task-1")
	e.mustSend("task-1", workflow.EventBuilderStarted, "{}\n")
	e.work(1)
	e.settle(att)
	e.mustSend("task-1", workflow.EventBuilderSubmitted, "{\"message\":\"done\"}\n")

	cctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- e.c.Tick(cctx, e.runID) }()
	var child int
	for i := 0; i < 200 && child == 0; i++ {
		time.Sleep(50 * time.Millisecond)
		if raw, err := os.ReadFile(pidfile); err == nil {
			child, _ = strconv.Atoi(strings.TrimSpace(string(raw)))
		}
	}
	if child == 0 {
		t.Fatal("the reviewer never started")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the tick did not return after cancellation")
	}
	gone := false
	for i := 0; i < 60 && !gone; i++ {
		gone = syscall.Kill(child, 0) != nil
		time.Sleep(50 * time.Millisecond)
	}
	if !gone {
		syscall.Kill(child, syscall.SIGKILL)
		t.Error("the reviewer's child outlived the cancelled job")
	}
	// A cancelled review is neither a pass nor a failure to be acted on.
	if got := e.reviewState(att); got == workflow.ReviewPassed || e.tasks()[0].AcceptedAttemptID != "" {
		t.Errorf("review %s after cancellation", got)
	}
	if _, found, _ := e.st.LatestReviewResult(ctx, att.ID); found {
		t.Error("cancellation recorded a result")
	}
	_ = errors.New
}

// A direct reviewer's usage is counted under the reviewer's provider, not the
// builder's.
func TestDirectReviewSourceIsRegisteredUnderTheReviewersProvider(t *testing.T) {
	e := directEnv(t, reviewerScript(t, passing))
	e.startInCollaboration()
	att := e.toReview("task-1", 1)
	jobs, _ := e.st.AttemptJobs(ctx, att.ID, "reviewer")
	if len(jobs) != 1 {
		t.Fatalf("%d reviewer jobs", len(jobs))
	}
	run, cfg, err := e.c.load(ctx, e.runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.c.registerDirectReview(ctx, run, cfg, att, jobs[0]); err != nil {
		t.Fatal(err)
	}
	srcs, err := e.st.ListSources(ctx, testCollab)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range srcs {
		if s.Role == "reviewer" && s.JobID == jobs[0].ID {
			if s.Provider != "codex" {
				t.Fatalf("the direct reviewer is registered under %q, want the reviewer's codex (builder is %s)", s.Provider, cfg.BuilderKind)
			}
			return
		}
	}
	t.Fatalf("no reviewer source for the job: %+v", srcs)
}

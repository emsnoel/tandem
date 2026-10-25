package controller

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/emsnoel/tandem/internal/artifact"
	"github.com/emsnoel/tandem/internal/check"
	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

var ctx = context.Background()

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// repo is a committed checkout with a dirty dependency, an ignored local file
// and two package directories.
func repo(t *testing.T) (root string, art *artifact.Store, run func(req CheckRequest) CheckResult) {
	t.Helper()
	root = filepath.Join(t.TempDir(), "repo")
	os.MkdirAll(root, 0o755)
	git(t, root, "init", "-q")
	write(t, root, ".gitignore", ".env.local\nnoise.log\n")
	write(t, root, "src/a.txt", "base-a\n")
	write(t, root, "dep/lib.txt", "base-lib\n")
	write(t, root, "pkgA/keep", "a")
	write(t, root, "pkgB/keep", "b")
	git(t, root, "add", ".")
	git(t, root, "commit", "-qm", "init")
	write(t, root, "dep/lib.txt", "dirty-lib\n") // already dirty before any check
	var err error
	art, err = artifact.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	exec := NewCheckExecutor(art)
	var n int
	run = func(req CheckRequest) CheckResult {
		t.Helper()
		n++
		if req.JobID == "" {
			req.JobID = hex32(n)
		}
		if req.AttemptID == "" {
			req.AttemptID = hex32(900)
		}
		req.Root = root
		res, err := exec(ctx, req)
		if err != nil {
			t.Fatalf("check: %v", err)
		}
		return res
	}
	return root, art, run
}

func hex32(n int) string { return strings.Repeat("0", 32-len(strconv.Itoa(n))) + strconv.Itoa(n) }

func TestCheckSamePorcelainDifferentBytesInvalidates(t *testing.T) {
	root, _, run := repo(t)
	a := run(CheckRequest{Command: "true"})
	b := run(CheckRequest{Command: "true"})
	if !a.Complete || a.BeforeInputsHash == "" || a.BeforeInputsHash != a.AfterInputsHash || a.BeforeInputsHash != b.BeforeInputsHash {
		t.Fatalf("an unchanged tree must fingerprint the same: %+v %+v", a, b)
	}
	// dep/lib.txt is " M" before and after: git status cannot tell these apart.
	write(t, root, "dep/lib.txt", "other-dirty-bytes\n")
	c := run(CheckRequest{Command: "true"})
	if c.BeforeInputsHash == a.BeforeInputsHash {
		t.Fatal("different bytes with the same porcelain status produced the same inputs hash")
	}
	// A file outside any task path counts just as much.
	write(t, root, "pkgB/keep", "changed outside the task")
	if d := run(CheckRequest{Command: "true"}); d.BeforeInputsHash == c.BeforeInputsHash {
		t.Fatal("a change outside the task's paths did not change the inputs hash")
	}
	// An ignored file that nothing declared is not an input.
	write(t, root, "noise.log", "x")
	e1 := run(CheckRequest{Command: "true"})
	write(t, root, "noise.log", "something else entirely")
	if e2 := run(CheckRequest{Command: "true"}); e2.BeforeInputsHash != e1.BeforeInputsHash {
		t.Fatal("an undeclared ignored file changed the hash")
	}
}

func TestCheckIdenticalCommandsInDifferentDirectoriesDiffer(t *testing.T) {
	_, _, run := repo(t)
	a := run(CheckRequest{Command: "true", Cwd: "pkgA"})
	b := run(CheckRequest{Command: "true", Cwd: "pkgB"})
	if a.CommandHash == b.CommandHash || a.BeforeInputsHash == b.BeforeInputsHash {
		t.Fatal("the working directory is part of what was verified")
	}
	if c := run(CheckRequest{Command: "true", Args: []string{"--flag"}, Cwd: "pkgA"}); c.CommandHash == a.CommandHash {
		t.Fatal("arguments are part of what was verified")
	}
	if d := run(CheckRequest{Command: "true", Cwd: "pkgA"}); d.CommandHash != a.CommandHash {
		t.Fatal("the same command and directory must hash the same")
	}
}

func TestCheckDeclaredInputsBindEnvironmentFilesAndTools(t *testing.T) {
	root, art, run := repo(t)
	tool := filepath.Join(t.TempDir(), "mytool")
	os.WriteFile(tool, []byte("#!/bin/sh\necho v1\n"), 0o755)
	write(t, root, ".env.local", "LOCAL=1")
	t.Setenv("TANDEM_TEST_DECLARED", "secret-value-one")
	req := CheckRequest{Command: "true", Inputs: CheckInputs{Env: []string{"TANDEM_TEST_DECLARED"}, Files: []string{".env.local"}, Tools: []string{tool}}}
	base := run(req)
	if !base.Complete || base.EnvironmentHash == "" {
		t.Fatalf("%+v", base)
	}

	t.Setenv("TANDEM_TEST_DECLARED", "secret-value-two")
	if r := run(req); r.BeforeInputsHash == base.BeforeInputsHash || r.EnvironmentHash == base.EnvironmentHash {
		t.Fatal("a declared environment variable's value is an input")
	}
	t.Setenv("TANDEM_TEST_DECLARED", "secret-value-one")
	write(t, root, ".env.local", "LOCAL=2")
	if r := run(req); r.BeforeInputsHash == base.BeforeInputsHash {
		t.Fatal("a declared ignored file is an input")
	}
	write(t, root, ".env.local", "LOCAL=1")
	os.WriteFile(tool, []byte("#!/bin/sh\necho v2\n"), 0o755)
	if r := run(req); r.BeforeInputsHash == base.BeforeInputsHash {
		t.Fatal("a declared tool's bytes are an input")
	}
	os.WriteFile(tool, []byte("#!/bin/sh\necho v1\n"), 0o755)
	os.Remove(filepath.Join(root, ".env.local"))
	if r := run(req); r.BeforeInputsHash == base.BeforeInputsHash {
		t.Fatal("a declared file going missing is a change")
	}

	// Values are stored as hashes, never as the values.
	write(t, root, ".env.local", "LOCAL=1")
	run(req)
	filepath.Walk(filepath.Join(art.ScratchDir(), ".."), func(p string, info os.FileInfo, err error) error {
		if err == nil && info.Mode().IsRegular() {
			if body, _ := os.ReadFile(p); bytes.Contains(body, []byte("secret-value-one")) || bytes.Contains(body, []byte("secret-value-two")) {
				t.Errorf("%s holds a declared environment value", p)
			}
		}
		return nil
	})
	// Undeclared and unusable declarations are refused, not skipped.
	exec := NewCheckExecutor(art)
	for name, in := range map[string]CheckInputs{
		"file outside the checkout": {Files: []string{"../elsewhere"}},
		"absolute file":             {Files: []string{"/etc/hosts"}},
		"unknown tool":              {Tools: []string{"definitely-not-a-real-tool-xyz"}},
	} {
		if _, err := exec(ctx, CheckRequest{AttemptID: hex32(900), JobID: hex32(700), Root: root, Command: "true", Inputs: in}); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestCheckSummaryBoundsSingleHugeLine(t *testing.T) {
	_, art, run := repo(t)
	res := run(CheckRequest{Command: `head -c 20971520 /dev/zero | tr '\0' x; echo; echo "FAIL: boom"; exit 1`})
	if res.Exit != 1 || res.Valid() {
		t.Fatalf("%+v", res)
	}
	if len(res.Summary) > check.MaxSummaryBytes || !strings.Contains(res.Summary, "FAIL: boom") || !strings.Contains(res.Summary, res.LogHash) {
		t.Fatalf("summary (%d bytes) must be bounded, name the failure and the log artifact:\n%.600s", len(res.Summary), res.Summary)
	}
	c, err := art.Read(ctx, res.LogHash, 0, 1)
	if err != nil || c.TotalBytes < 20<<20 {
		t.Fatalf("the complete log was not retained: total %d err %v", c.TotalBytes, err)
	}
	if left, _ := os.ReadDir(art.ScratchDir()); len(left) != 0 {
		t.Fatalf("the check's scratch log was left behind: %v", left)
	}
}

func TestCheckRetainsTheFullLogOfAFailure(t *testing.T) {
	_, art, run := repo(t)
	res := run(CheckRequest{Command: `echo out; echo err >&2; exit 3`})
	if res.Exit != 3 || res.Valid() || !res.Complete {
		t.Fatalf("%+v", res)
	}
	log, err := art.ReadAll(ctx, res.LogHash, 1<<20)
	if err != nil || !strings.Contains(string(log), "out") || !strings.Contains(string(log), "err") {
		t.Fatalf("log %q %v", log, err)
	}
	if res.JobID != hex32(1) || res.AttemptID != hex32(900) {
		t.Fatalf("identity %+v", res)
	}
}

func TestCheckCancellationStopsChildProcess(t *testing.T) {
	root, art, _ := repo(t)
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	exec := NewCheckExecutor(art)
	cctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		_, err := exec(cctx, CheckRequest{AttemptID: hex32(900), JobID: hex32(5), Root: root,
			Command: "sleep 300 & echo $! > " + pidFile + "; wait"})
		done <- err
	}()
	var pid int
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if raw, err := os.ReadFile(pidFile); err == nil && strings.TrimSpace(string(raw)) != "" {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(raw)))
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("the fixture's child never started")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the executor did not return after cancellation")
	}
	// The spawned child shares the check's process group and must die with it.
	for i := 0; i < 100; i++ {
		if err := syscall.Kill(pid, 0); err != nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	syscall.Kill(pid, syscall.SIGKILL)
	t.Fatalf("child %d survived the cancellation", pid)
}

func TestCheckTimeoutIsAFailureWithItsLog(t *testing.T) {
	root, art, _ := repo(t)
	tctx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	res, err := NewCheckExecutor(art)(tctx, CheckRequest{AttemptID: hex32(900), JobID: hex32(6), Root: root, Command: "echo started; sleep 30"})
	if err != nil {
		t.Fatalf("a timeout is a result, not an error: %v", err)
	}
	if !res.TimedOut || res.Exit != check.TimeoutExit || res.Valid() || !strings.Contains(res.Summary, "timed out") {
		t.Fatalf("%+v", res)
	}
}

func TestCheckMutationDoesNotCertifyNewTree(t *testing.T) {
	_, _, run := repo(t)
	res := run(CheckRequest{Command: `echo "edited by the check" >> src/a.txt`})
	if res.Exit != 0 || !res.Complete {
		t.Fatalf("%+v", res)
	}
	if res.BeforeInputsHash == res.AfterInputsHash || !res.Mutated() || res.Valid() {
		t.Fatalf("a check that edits source must not certify the tree it left: %+v", res)
	}
	if !strings.Contains(strings.Join(res.Errors, "\n"), "changed") {
		t.Fatalf("the mutation must be diagnosed: %v", res.Errors)
	}
	// Rerunning on the edited tree is a clean verification and may certify it.
	clean := run(CheckRequest{Command: "true"})
	if !clean.Valid() || clean.Mutated() {
		t.Fatalf("%+v", clean)
	}
}

func TestCheckRefusesMalformedRequests(t *testing.T) {
	root, art, _ := repo(t)
	exec := NewCheckExecutor(art)
	outside := t.TempDir()
	os.Symlink(outside, filepath.Join(root, "escape"))
	good := CheckRequest{AttemptID: hex32(900), JobID: hex32(8), Root: root, Command: "true"}
	for name, mutate := range map[string]func(*CheckRequest){
		"job id is a path":        func(r *CheckRequest) { r.JobID = "../../x" },
		"short attempt id":        func(r *CheckRequest) { r.AttemptID = "abc" },
		"empty command":           func(r *CheckRequest) { r.Command = " " },
		"cwd with ..":             func(r *CheckRequest) { r.Cwd = "../.." },
		"absolute cwd":            func(r *CheckRequest) { r.Cwd = "/tmp" },
		"cwd through a symlink":   func(r *CheckRequest) { r.Cwd = "escape" },
		"cwd that does not exist": func(r *CheckRequest) { r.Cwd = "nope" },
		"not a git checkout":      func(r *CheckRequest) { r.Root = t.TempDir() },
	} {
		req := good
		mutate(&req)
		if _, err := exec(ctx, req); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if left, _ := os.ReadDir(art.ScratchDir()); len(left) != 0 {
		t.Errorf("a refused request left scratch files: %v", left)
	}
}

// seedAttempt builds the rows an attempt that has submitted needs, with plain
// SQL and foreign keys on, in the store at stateRoot.
func seedAttempt(t *testing.T, stateRoot string, art *artifact.Store) (runID, attemptID, endHash string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(stateRoot, "state.sqlite")+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	put := func(s string) string {
		h, err := art.Put(ctx, strings.NewReader(s))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT OR IGNORE INTO artifacts(hash,size_bytes,media_type,created_ms) VALUES(?,?, 'text/plain',1)`, h, len(s)); err != nil {
			t.Fatal(err)
		}
		return h
	}
	runID, attemptID = hex32(101), hex32(102)
	endHash = put("end snapshot")
	patch, plan, cfg, brief := put("patch"), put("plan"), put("config"), put("brief")
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO checkouts(id,canonical_root,git_common_dir,created_ms) VALUES(?, '/c', '/c/.git', 1)`, []any{hex32(103)}},
		{`INSERT INTO projects(id,checkout_id,manifest_root) VALUES(?,?, '.')`, []any{hex32(104), hex32(103)}},
		{`INSERT INTO runs(id,project_id,plan_hash,config_hash,engine_version,state,created_ms) VALUES(?,?,?,?,2,'running',1)`, []any{runID, hex32(104), plan, cfg}},
		{`INSERT INTO tasks(run_id,id,ordinal,brief_hash,constraints_hash,spec_hash) VALUES(?, 'task-1',1,?,?,?)`, []any{runID, brief, brief, brief}},
		{`INSERT INTO attempts(id,run_id,task_id,ordinal,implementation_state,check_state,review_state,budget_state,start_snapshot_hash,end_snapshot_hash,patch_hash,version,is_current,created_ms)
			VALUES(?,?, 'task-1',1,'submitted','pending','pending','available',?,?,?,3,1,1)`, []any{attemptID, runID, endHash, endHash, patch}},
	} {
		if _, err := db.Exec(q.sql, q.args...); err != nil {
			t.Fatalf("seed %q: %v", q.sql, err)
		}
	}
	return runID, attemptID, endHash
}

func openState(t *testing.T) (*state.Store, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "state")
	s, err := state.Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, root
}

func TestStartAndRecordCheckPersistThroughTheTrustedPath(t *testing.T) {
	for _, tc := range []struct {
		name, command string
		wantCheck     string
		wantErr       error
	}{
		{"pass", "true", "passed", nil},
		{"fail", "exit 2", "failed", nil},
		{"mutating", `echo x >> src/a.txt`, "pending", ErrSourceMutated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, _, _ := repo(t)
			st, stRoot := openState(t)
			art, _ := artifact.Open(stRoot)
			runID, attemptID, endHash := seedAttempt(t, stRoot, art)

			req, err := StartCheck(ctx, st, art, CheckSpec{AttemptID: attemptID, InputHash: endHash, Root: root, Command: tc.command})
			if err != nil {
				t.Fatal(err)
			}
			var jobState string
			db, _ := sql.Open("sqlite", "file:"+filepath.Join(stRoot, "state.sqlite"))
			defer db.Close()
			db.QueryRow(`SELECT state FROM jobs WHERE id=?`, req.JobID).Scan(&jobState)
			if jobState != "running" {
				t.Fatalf("the job must exist before the check launches; state %q", jobState)
			}
			res, err := NewCheckExecutor(art)(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			rec, err := RecordCheck(ctx, st, art, RecordSpec{RunID: runID, ExpectedVersion: 3, EventID: hex32(300)}, res, workflow.Observed{SchedulingAllowed: true, NoReviewPolicy: true})
			if !errors.Is(err, tc.wantErr) && !(tc.wantErr == nil && err == nil) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			var check string
			var certs int
			db.QueryRow(`SELECT check_state FROM attempts WHERE id=?`, attemptID).Scan(&check)
			db.QueryRow(`SELECT count(*) FROM check_certificates WHERE job_id=?`, req.JobID).Scan(&certs)
			db.QueryRow(`SELECT state FROM jobs WHERE id=?`, req.JobID).Scan(&jobState)
			if check != tc.wantCheck || certs != 1 || jobState != "completed" {
				t.Fatalf("check %s certificates %d job %s", check, certs, jobState)
			}
			if tc.wantErr == nil && (rec.Version != 4 || rec.Duplicate) {
				t.Fatalf("receipt %+v", rec)
			}
			if errors.Is(err, ErrSourceMutated) && !errors.Is(err, workflow.ErrIncompleteEvidence) {
				t.Fatal("a mutation must also read as incomplete evidence")
			}
			if tc.wantErr == nil {
				// Retrying the same recording is harmless and returns the same receipt.
				again, err := RecordCheck(ctx, st, art, RecordSpec{RunID: runID, ExpectedVersion: 3, EventID: hex32(300)}, res, workflow.Observed{SchedulingAllowed: true, NoReviewPolicy: true})
				if err != nil || !again.Duplicate || again.Version != rec.Version {
					t.Fatalf("retry: %+v %v", again, err)
				}
			}
		})
	}
}

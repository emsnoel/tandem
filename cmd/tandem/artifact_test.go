package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emsnoel/tandem/internal/controller"
	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/paths"
	"github.com/emsnoel/tandem/internal/state"
)

// attemptInfo is what a builder knows from its prompt, read from the state the
// controller keeps.
type attemptInfo struct {
	run, attempt, packet, job, effect, nonce string
}

func (e *cliEnv) attempt(taskID string) attemptInfo {
	e.t.Helper()
	ar, err := controller.ReadActiveRun(paths.Relay(e.root))
	if err != nil || ar == nil {
		e.t.Fatal(ar, err)
	}
	id, bound, err := state.LookupIdentity(e.root, e.home)
	if err != nil || !bound {
		e.t.Fatal(bound, err)
	}
	st, err := state.Open(context.Background(), id.StateRoot)
	if err != nil {
		e.t.Fatal(err)
	}
	defer st.Close()
	as, err := st.TaskAttempts(context.Background(), ar.RunID, taskID)
	if err != nil || len(as) == 0 {
		e.t.Fatal(as, err)
	}
	a := as[len(as)-1]
	jobs, _ := st.AttemptJobs(context.Background(), a.ID, "builder")
	effs, _ := st.RunEffects(context.Background(), ar.RunID)
	var eff, nonce string
	for _, x := range effs {
		if x.AttemptID == a.ID && x.Kind == "builder_dispatch" {
			eff, nonce = x.ID, x.Nonce
		}
	}
	return attemptInfo{ar.RunID, a.ID, a.PacketHash, jobs[0].ID, eff, nonce}
}

func startedCLI(t *testing.T) (*cliEnv, attemptInfo) {
	t.Helper()
	e := newCLI(t)
	t.Setenv("TANDEM_BUILD_ENGINE", "durable")
	if rc, _, errOut := e.start(); rc != exitcode.OK {
		t.Fatalf("start: %d %s", rc, errOut)
	}
	e.sub("tick")
	return e, e.attempt("task-1")
}

func (e *cliEnv) artifactRead(hash string, extra ...string) (int, map[string]any, string, string) {
	e.t.Helper()
	args := append([]string{"artifact", "read", hash, "--root", e.root, "--state-home", e.home}, extra...)
	rc, out, errOut := e.run(args...)
	var got map[string]any
	if strings.TrimSpace(out) != "" {
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			e.t.Fatalf("stdout is not one JSON object: %q", out)
		}
	}
	return rc, got, out, errOut
}

func TestArtifactReadDirectThroughTheCLI(t *testing.T) {
	e, a := startedCLI(t)
	rc, got, _, errOut := e.artifactRead(a.packet, "--attempt", a.attempt, "--offset", "10", "--limit", "40")
	if rc != exitcode.OK {
		t.Fatalf("rc %d %s", rc, errOut)
	}
	if got["hash"] != a.packet || got["encoding"] != "utf8" || got["offset"] != float64(10) || got["next_offset"] != float64(50) || got["total_bytes"].(float64) < 100 ||
		len(got["text"].(string)) != 40 || got["limit"] != float64(40) {
		t.Errorf("%v", got)
	}
	// A limit above the cap is cut to it.
	if rc, got, _, _ := e.artifactRead(a.packet, "--attempt", a.attempt, "--limit", "999999"); rc != exitcode.OK || got["limit"] != float64(32768) {
		t.Errorf("rc %d %v", rc, got["limit"])
	}
	// Refusals return no bytes.
	for name, args := range map[string][]string{
		"an artifact of the run that is not the attempt's": {"--attempt", a.attempt},
		"another attempt": {"--attempt", strings.Repeat("0", 32)},
	} {
		h := a.packet
		if strings.HasPrefix(name, "an artifact") {
			h = strings.Repeat("9", 64)
		}
		rc, got, out, errOut := e.artifactRead(h, args...)
		if rc != exitcode.Internal || got != nil || strings.Contains(out, "Implement task") || !strings.Contains(errOut, "not available") {
			t.Errorf("%s: rc %d stdout %q stderr %q", name, rc, out, errOut)
		}
	}
	// Bad arguments are usage errors.
	for name, args := range map[string][]string{
		"negative offset": {a.packet, "--attempt", a.attempt, "--offset", "-1"},
		"negative limit":  {a.packet, "--attempt", a.attempt, "--limit", "-1"},
		"short hash":      {"abc", "--attempt", a.attempt},
		"no attempt":      {a.packet},
		"unknown flag":    {a.packet, "--attempt", a.attempt, "--path", "/etc/passwd"},
		"bad via":         {a.packet, "--attempt", a.attempt, "--via", "ssh"},
	} {
		if rc, _, _ := e.run(append([]string{"artifact", "read"}, append(args, "--root", e.root, "--state-home", e.home)...)...); rc != exitcode.Usage {
			t.Errorf("%s: rc %d", name, rc)
		}
	}
}

func TestArtifactReadRefusesACorruptArtifact(t *testing.T) {
	e, a := startedCLI(t)
	var obj string
	filepath.Walk(e.home, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && info.Name() == a.packet {
			obj = p
		}
		return nil
	})
	if obj == "" {
		t.Fatal("the packet object was not found")
	}
	if err := os.Chmod(obj, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(obj, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	rc, got, out, errOut := e.artifactRead(a.packet, "--attempt", a.attempt)
	if rc == exitcode.OK || got != nil || strings.Contains(out, "tampered") {
		t.Errorf("a corrupt artifact was served: rc %d %q %q", rc, out, errOut)
	}
}

// sandboxArgs reads through the mailbox from a state home the caller cannot see.
func (e *cliEnv) sandboxRead(a attemptInfo, hash string, extra ...string) (int, map[string]any, string, string) {
	e.t.Helper()
	args := append([]string{"artifact", "read", hash, "--root", e.root, "--state-home", filepath.Join(e.t.TempDir(), "unreadable"),
		"--attempt", a.attempt, "--task", "1", "--job", a.job, "--effect", a.effect, "--nonce", a.nonce}, extra...)
	rc, out, errOut := e.run(args...)
	var got map[string]any
	if strings.TrimSpace(out) != "" {
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			e.t.Fatalf("stdout is not one JSON object: %q", out)
		}
	}
	return rc, got, out, errOut
}

// serve is the controller side of a mailbox read: one tick, as the bridge gives
// it, without going through a command that captures standard output (two of
// those at once would race on it).
func (e *cliEnv) serve() {
	e.t.Helper()
	d, err := openDurable(e.root, e.home, false)
	if err != nil {
		e.t.Error(err)
		return
	}
	defer d.close()
	c, err := d.controller(noSend{})
	if err != nil {
		e.t.Error(err)
		return
	}
	if err := c.Tick(context.Background(), d.run.RunID); err != nil {
		e.t.Error(err)
	}
}

func queued(e *cliEnv, run string) []string {
	var out []string
	for _, pat := range []string{"*.json", "*.json.taken"} {
		m, _ := filepath.Glob(filepath.Join(paths.Relay(e.root), "durable", run, "q", pat))
		out = append(out, m...)
	}
	return out
}

func TestArtifactSandboxReadUsesMailbox(t *testing.T) {
	e, a := startedCLI(t)
	// The controller side, running with access to the state, serves the request a
	// moment after it is queued.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 60; i++ {
			time.Sleep(100 * time.Millisecond)
			if len(queued(e, a.run)) > 0 {
				e.serve()
				return
			}
		}
	}()
	rc, got, out, errOut := e.sandboxRead(a, a.packet, "--offset", "0", "--limit", "64", "--wait", "10")
	<-done
	if rc != exitcode.OK {
		t.Fatalf("rc %d stdout %q stderr %q", rc, out, errOut)
	}
	rid, _ := got["request_id"].(string)
	if got["hash"] != a.packet || len(rid) != 32 || got["encoding"] != "utf8" || len(got["text"].(string)) != 64 || got["next_offset"] != float64(64) || got["status"] != "ok" {
		t.Errorf("%v", got)
	}
	// The chunk reached the caller through the relay, and nothing else did.
	resp := filepath.Join(paths.Relay(e.root), "durable", a.run, "responses", rid+".json")
	if raw, err := os.ReadFile(resp); err != nil || len(raw) > 4096 {
		t.Errorf("response: %d bytes, %v", len(raw), err)
	}
	// Asking again does not queue a second request.
	rc, again, _, _ := e.sandboxRead(a, a.packet, "--offset", "0", "--limit", "64", "--wait", "2")
	if rc != exitcode.OK || again["request_id"] != rid || len(queued(e, a.run)) != 0 {
		t.Errorf("rc %d %v queued %v", rc, again["request_id"], queued(e, a.run))
	}
	// A hash the attempt may not read is refused through the same route.
	go func() {
		for i := 0; i < 60; i++ {
			time.Sleep(100 * time.Millisecond)
			if len(queued(e, a.run)) > 0 {
				e.serve()
				return
			}
		}
	}()
	rc, refused, out, errOut := e.sandboxRead(a, strings.Repeat("9", 64), "--wait", "10")
	if rc != exitcode.Internal || strings.Contains(out, "Implement") || !strings.Contains(errOut, "not available") {
		t.Errorf("rc %d %v %q %q", rc, refused, out, errOut)
	}
}

func TestArtifactReadTimeoutRetainsRequest(t *testing.T) {
	e, a := startedCLI(t)
	rc, got, _, errOut := e.sandboxRead(a, a.packet, "--limit", "32", "--wait", "1")
	if rc != exitcode.Internal || got["status"] != "pending" || !strings.Contains(errOut, "--request") {
		t.Fatalf("rc %d %v %q", rc, got, errOut)
	}
	rid := got["request_id"].(string)
	if len(rid) != 32 || len(queued(e, a.run)) != 1 {
		t.Fatalf("request %q, %d queued", rid, len(queued(e, a.run)))
	}
	// The same request again is the same request: one file, same id.
	rc, got2, _, _ := e.sandboxRead(a, a.packet, "--limit", "32", "--wait", "0")
	if rc != exitcode.Internal || got2["request_id"] != rid || len(queued(e, a.run)) != 1 {
		t.Errorf("rc %d %v, %d queued", rc, got2["request_id"], len(queued(e, a.run)))
	}
	// A different window is a different request.
	if _, got3, _, _ := e.sandboxRead(a, a.packet, "--limit", "16", "--wait", "0"); got3["request_id"] == rid || len(queued(e, a.run)) != 2 {
		t.Errorf("%v %d", got3["request_id"], len(queued(e, a.run)))
	}
	// Inspecting creates nothing and says it is still waiting.
	rc, got4, _, _ := e.run3("artifact", "read", "--request", rid, "--root", e.root)
	if rc != exitcode.Internal || got4["status"] != "pending" || len(queued(e, a.run)) != 2 {
		t.Errorf("inspect: rc %d %v", rc, got4)
	}
	// The controller comes back; the answer is there for the same id.
	e.sub("tick")
	rc, got5, _, errOut := e.run3("artifact", "read", "--request", rid, "--root", e.root)
	if rc != exitcode.OK || got5["status"] != "ok" || got5["request_id"] != rid || len(got5["text"].(string)) != 32 {
		t.Fatalf("inspect after: rc %d %v %q", rc, got5, errOut)
	}
	if rc, got6, _, _ := e.sandboxRead(a, a.packet, "--limit", "32", "--wait", "0"); rc != exitcode.OK || got6["request_id"] != rid || len(queued(e, a.run)) != 0 {
		t.Errorf("a repeat after the answer: rc %d %v", rc, queued(e, a.run))
	}
	rc, _, out, errOut := e.run3("artifact", "read", "--request", strings.Repeat("1", 32), "--root", e.root)
	if rc != exitcode.Internal || out != "" || !strings.Contains(errOut, "no request") {
		t.Errorf("an unknown request id: rc %d %q %q", rc, out, errOut)
	}
	if rc, _, _, _ := e.run3("artifact", "read", "--request", "../../etc", "--root", e.root); rc != exitcode.Usage {
		t.Errorf("a malformed request id: rc %d", rc)
	}
}

func TestArtifactMailboxReadNeedsTheBuilderIdentity(t *testing.T) {
	e, a := startedCLI(t)
	base := []string{"artifact", "read", a.packet, "--root", e.root, "--state-home", filepath.Join(t.TempDir(), "none"), "--via", "mailbox", "--attempt", a.attempt}
	if rc, _, errOut := e.run(base...); rc != exitcode.Usage || !strings.Contains(errOut, "--nonce") {
		t.Errorf("rc %d %q", rc, errOut)
	}
	if rc, _, errOut := e.run(append(base, "--task", "1", "--job", a.job, "--effect", a.effect, "--nonce", "short")...); rc != exitcode.Usage {
		t.Errorf("a malformed nonce: rc %d %q", rc, errOut)
	}
	if len(queued(e, a.run)) != 0 {
		t.Error("a malformed request was queued")
	}
	// With the state home unreadable and no --via, the CLI uses the mailbox.
	rc, got, _, _ := e.sandboxRead(a, a.packet, "--wait", "0")
	if rc != exitcode.Internal || got["status"] != "pending" || len(queued(e, a.run)) != 1 {
		t.Errorf("auto fallback: rc %d %v", rc, got)
	}
}

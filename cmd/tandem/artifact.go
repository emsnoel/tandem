package main

// tandem artifact read: bounded evidence retrieval for a builder.
//
// 	tandem artifact read HASH --attempt ID [--offset N] [--limit N]
// 	tandem artifact read --request ID
//
// The attempt may read only what the controller allows
// (internal/controller/artifact_request.go). With a readable state root the
// read is direct. Otherwise the request goes through the relay and this
// process waits up to 30 seconds for the controller's answer. A request that
// times out keeps its id, so asking again or --request finds it.
//
// Standard output is one JSON object; diagnostics go to standard error.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/emsnoel/tandem/internal/artifact"
	"github.com/emsnoel/tandem/internal/controller"
	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/paths"
	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/vcs"
)

const (
	maxWait       = 30 * time.Second
	waitStep      = 100 * time.Millisecond
	artifactUse   = "usage: tandem artifact read HASH --attempt ID [--offset N] [--limit N] [--root DIR] [--state-home DIR] [--via auto|direct|mailbox] [--wait SECONDS]\n       tandem artifact read --request ID [--root DIR] [--wait SECONDS]\n       in a sandbox the mailbox route also needs --task N --job ID --effect ID --nonce ID, as in the prompt"
	maxResponseKB = 128
)

var (
	idRE   = regexp.MustCompile(`^[0-9a-f]{32}$`)
	hashRE = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// artifactOut is the one object printed: a chunk, with the request id when it
// came through the mailbox.
type artifactOut struct {
	Status    string `json:"status"`
	RequestID string `json:"request_id,omitempty"`
	controller.ArtifactResult
}

func cmdArtifact(args []string) int {
	if len(args) == 0 || args[0] != "read" {
		fmt.Fprintln(os.Stderr, artifactUse)
		return exitcode.Usage
	}
	return cmdArtifactRead(args[1:])
}

func cmdArtifactRead(args []string) int {
	// The hash is positional and flags follow it, so take it off first.
	hash := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		hash, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet("artifact read", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var f durableFlags
	f.register(fs)
	attempt := fs.String("attempt", "", "the attempt reading")
	offset := fs.Int64("offset", 0, "first byte")
	limit := fs.Int("limit", 0, "most bytes; 0 is 32768, which is also the most")
	via := fs.String("via", "auto", "auto, direct or mailbox")
	wait := fs.Int("wait", 30, "seconds to wait for the controller, at most 30, mailbox only")
	request := fs.String("request", "", "inspect an earlier mailbox request by id")
	task := fs.Int("task", 0, "task number (mailbox)")
	job := fs.String("job", "", "builder job id (mailbox)")
	effect := fs.String("effect", "", "builder dispatch effect id (mailbox)")
	nonce := fs.String("nonce", "", "the nonce the prompt carried (mailbox)")
	usage := func(msg string) int {
		if msg != "" {
			fmt.Fprintln(os.Stderr, "tandem artifact read:", msg)
		}
		fmt.Fprintln(os.Stderr, artifactUse)
		return exitcode.Usage
	}
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return usage("")
	}
	if *wait < 0 {
		return usage("--wait cannot be negative")
	}
	waitFor := min(time.Duration(*wait)*time.Second, maxWait)
	if f.root == "" {
		wd, _ := os.Getwd()
		top, err := vcs.Toplevel(wd)
		if err != nil {
			return usage("not inside a git checkout; pass --root")
		}
		f.root = top
	}
	if *request != "" {
		if hash != "" || *attempt != "" {
			return usage("--request inspects an earlier request and takes nothing else")
		}
		if !idRE.MatchString(*request) {
			return usage("a request id is 32 lowercase hex characters")
		}
		return inspectRequest(f.root, *request, 0)
	}
	switch {
	case !hashRE.MatchString(hash):
		return usage("a hash is 64 lowercase hex characters")
	case !idRE.MatchString(*attempt):
		return usage("--attempt must be an attempt id")
	case *offset < 0 || *limit < 0:
		return usage("--offset and --limit cannot be negative")
	case *via != "auto" && *via != "direct" && *via != "mailbox":
		return usage("--via is auto, direct or mailbox")
	}
	req := controller.ArtifactRequest{Hash: hash, Offset: *offset, Limit: *limit}

	if *via != "mailbox" {
		code, ok := readDirect(f, *attempt, req)
		if ok || *via == "direct" {
			return code
		}
		// The state cannot be reached from here: use the relay.
	}
	if !idRE.MatchString(*job) || !idRE.MatchString(*effect) || !idRE.MatchString(*nonce) || *task < 1 {
		return usage("through the mailbox a read must carry the builder's identity from its prompt: --task N --job ID --effect ID --nonce ID")
	}
	return readThroughMailbox(f.root, *attempt, *task, *job, *effect, *nonce, req, waitFor)
}

// readDirect serves the read from the state. ok is false only when the state
// could not be reached; any other outcome, a refusal included, is final.
func readDirect(f durableFlags, attempt string, req controller.ArtifactRequest) (code int, ok bool) {
	run, err := controller.ReadActiveRun(paths.Relay(f.root))
	if err != nil || run == nil {
		return fail(errors.New("no durable run in this checkout")), true
	}
	id, bound, err := state.LookupIdentity(f.root, f.home)
	if err != nil || !bound {
		return 0, false
	}
	ctx := context.Background()
	st, err := state.OpenReadOnly(ctx, id.StateRoot)
	if err != nil {
		return 0, false
	}
	defer st.Close()
	art, err := artifact.Open(id.StateRoot)
	if err != nil {
		return 0, false
	}
	res, err := controller.ReadArtifact(ctx, st, art, run.RunID, attempt, req)
	switch {
	case errors.Is(err, artifact.ErrInvalidRange), errors.Is(err, artifact.ErrInvalidHash):
		fmt.Fprintln(os.Stderr, "tandem artifact read:", err)
		return exitcode.Usage, true
	case err != nil:
		return fail(err), true
	}
	return printJSON(artifactOut{Status: "ok", ArtifactResult: res}), true
}

// requestID makes a request's id from what it asks for, so asking for the same
// thing twice is the same request.
func requestID(run, attempt string, req controller.ArtifactRequest) (string, []byte) {
	body, _ := json.Marshal(struct {
		Hash   string `json:"hash"`
		Offset int64  `json:"offset"`
		Limit  int    `json:"limit"`
	}{req.Hash, req.Offset, req.Limit})
	sum := sha256.Sum256([]byte(run + "|" + attempt + "|" + string(body)))
	return hex.EncodeToString(sum[:16]), body
}

func readThroughMailbox(root, attempt string, task int, job, effect, nonce string, req controller.ArtifactRequest, wait time.Duration) int {
	relay := paths.Relay(root)
	run, err := controller.ReadActiveRun(relay)
	if err != nil || run == nil {
		return fail(errors.New("no durable run in this checkout"))
	}
	dir := controller.RunDir(relay, run.RunID)
	taskID, err := controller.ResolveTask(dir, task)
	if err != nil {
		return fail(err)
	}
	id, body := requestID(run.RunID, attempt, req)
	if !requestExists(dir, id) {
		sum := sha256.Sum256(body)
		name := id + ".json"
		env := controller.Envelope{SchemaVersion: 2, EventID: id, RunID: run.RunID, TaskID: taskID, AttemptID: attempt, JobID: job, EffectID: effect, Nonce: nonce,
			Kind: "artifact_request", PayloadPath: "payloads/" + name, PayloadHash: hex.EncodeToString(sum[:])}
		raw, err := json.Marshal(env)
		if err != nil {
			return fail(err)
		}
		if _, err := controller.ParseEnvelope(raw); err != nil {
			fmt.Fprintln(os.Stderr, "tandem artifact read:", err)
			return exitcode.Usage
		}
		// Payload first, then the envelope that names it.
		if err := writeAtomic(filepath.Join(dir, "payloads"), name, body); err != nil {
			return fail(err)
		}
		if err := writeAtomic(filepath.Join(dir, "q"), name, append(raw, '\n')); err != nil {
			return fail(err)
		}
	}
	return inspectRequest(root, id, wait)
}

// requestExists is true once a request has been queued, claimed, answered or
// set aside.
func requestExists(dir, id string) bool {
	for _, p := range []string{
		filepath.Join(dir, "q", id+".json"), filepath.Join(dir, "q", id+".json.taken"),
		filepath.Join(dir, "responses", id+".json"),
		filepath.Join(dir, "quarantine", id+".json.taken.reason"), filepath.Join(dir, "quarantine", id+".json.reason"),
	} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

// inspectRequest reports what became of a request, waiting up to wait for the
// controller to answer it. It never queues anything.
func inspectRequest(root, id string, wait time.Duration) int {
	run, err := controller.ReadActiveRun(paths.Relay(root))
	if err != nil || run == nil {
		return fail(errors.New("no durable run in this checkout"))
	}
	dir := controller.RunDir(paths.Relay(root), run.RunID)
	deadline := time.Now().Add(wait)
	for {
		if raw, err := os.ReadFile(filepath.Join(dir, "responses", id+".json")); err == nil {
			if len(raw) > maxResponseKB<<10 {
				return fail(errors.New("the response is larger than expected"))
			}
			var resp controller.ArtifactResponse
			if err := json.Unmarshal(raw, &resp); err != nil {
				return fail(fmt.Errorf("the response is unreadable: %w", err))
			}
			if resp.Status == "ok" && resp.Result != nil {
				return printJSON(artifactOut{Status: "ok", RequestID: id, ArtifactResult: *resp.Result})
			}
			fmt.Fprintf(os.Stderr, "tandem artifact read: %s: %s\n", resp.Code, refusalText(resp))
			return exitcode.Internal
		}
		for _, p := range []string{filepath.Join(dir, "quarantine", id+".json.taken.reason"), filepath.Join(dir, "quarantine", id+".json.reason")} {
			if reason, err := os.ReadFile(p); err == nil {
				fmt.Fprintf(os.Stderr, "tandem artifact read: the request was rejected: %s\n", strings.TrimSpace(string(reason)))
				printJSON(map[string]string{"status": "rejected", "request_id": id})
				return exitcode.Internal
			}
		}
		if !requestExists(dir, id) {
			return fail(fmt.Errorf("no request %s in this run", id))
		}
		if !time.Now().Before(deadline) {
			fmt.Fprintf(os.Stderr, "tandem artifact read: the controller has not answered yet. Ask again later with `tandem artifact read --request %s`; the request is kept.\n", id)
			printJSON(map[string]string{"status": "pending", "request_id": id})
			return exitcode.Internal
		}
		time.Sleep(waitStep)
	}
}

func refusalText(r controller.ArtifactResponse) string {
	if r.Code == "artifact_not_available" {
		return "that artifact is not available to this attempt"
	}
	return r.Message
}

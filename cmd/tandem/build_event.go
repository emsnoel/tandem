package main

// tandem build event: the two halves of mailbox protocol 2 that shell scripts
// call, so no ID, hash or path rule is reimplemented in bash.
//
//	tandem build event new --root DIR --kind K --task N --attempt A --job J --effect E --nonce X [--payload FILE]
//	    run by the builder (tandem-handback): writes the payload and a versioned
//	    envelope into the run's mailbox, with a random event id.
//	tandem build event ingest --root DIR --claimed FILE [--state-home H] [--session-settled]
//	    run by the bridge on a file it has claimed: validates, imports, applies,
//	    records a receipt, removes the file or quarantines it.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/emsnoel/tandem/internal/controller"
	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/paths"
	"github.com/emsnoel/tandem/internal/workflow"
)

const eventUse = "usage: tandem build event new --root DIR --kind KIND --task N --attempt ID --job ID --effect ID --nonce ID [--payload FILE]\n" +
	"       tandem build event ingest --root DIR --claimed FILE [--state-home DIR] [--session-settled]"

func cmdBuildEvent(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, eventUse)
		return exitcode.Usage
	}
	switch args[0] {
	case "new":
		return cmdEventNew(args[1:])
	case "ingest":
		return cmdEventIngest(args[1:])
	}
	fmt.Fprintf(os.Stderr, "tandem build event: unknown subcommand %q\n%s\n", args[0], eventUse)
	return exitcode.Usage
}

func cmdEventNew(args []string) int {
	fs := flag.NewFlagSet("build event new", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	root := fs.String("root", "", "repository root")
	kind := fs.String("kind", "", "event kind")
	task := fs.Int("task", 0, "task number, resolved through the run manifest")
	attempt := fs.String("attempt", "", "attempt id")
	job := fs.String("job", "", "job id")
	effect := fs.String("effect", "", "effect id of the prompt being answered")
	nonce := fs.String("nonce", "", "nonce the prompt carried")
	payload := fs.String("payload", "", "file whose bytes travel with the event")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *root == "" || *kind == "" || *task < 1 {
		fmt.Fprintln(os.Stderr, eventUse)
		return exitcode.Usage
	}
	if !controller.IsBuilderKind(*kind) && !controller.IsReviewerKind(*kind) {
		fmt.Fprintf(os.Stderr, "tandem build event: %q is not an event a builder may send\n%s\n", *kind, eventUse)
		return exitcode.Usage
	}
	relay := paths.Relay(*root)
	run, err := controller.ReadActiveRun(relay)
	if err != nil {
		return fail(err)
	}
	if run == nil {
		return fail(fmt.Errorf("no durable run in %s", relay))
	}
	dir := controller.RunDir(relay, run.RunID)
	taskID, err := controller.ResolveTask(dir, *task)
	if err != nil {
		return fail(err)
	}
	manifest, err := controller.ReadManifest(dir)
	if err != nil {
		return fail(err)
	}
	version := 2
	// A reviewer's kind travels as a version 3 message only on a run that
	// opted in to direct handback; on any other run review_result keeps its
	// version 2 form.
	if controller.IsReviewerKind(*kind) && manifest.ReviewProtocol >= 3 {
		version = 3
	} else if *kind == controller.EventReviewStarted {
		fmt.Fprintf(os.Stderr, "tandem build event: run %s was not started with --review-delivery mailbox, so it takes no review_started\n", run.RunID)
		return exitcode.Usage
	}
	body := []byte(fmt.Sprintf("{\"kind\":%q}\n", *kind))
	if *kind == controller.EventReviewStarted {
		body = []byte("{}\n") // a start payload may only name a session
	}
	if *payload != "" {
		if body, err = readRegular(*payload, controller.MaxPayloadBytes); err != nil {
			return fail(err)
		}
	}
	if version >= 3 && *kind == controller.EventReviewResult {
		if err := workflow.ValidateVerdictRaw(body); err != nil {
			fmt.Fprintln(os.Stderr, "tandem build event:", err)
			return exitcode.Usage
		}
	}
	eventID, err := controller.NewEventID()
	if err != nil {
		return fail(err)
	}
	sum := sha256.Sum256(body)
	name := eventID + ".json"
	env := controller.Envelope{SchemaVersion: version, EventID: eventID, RunID: run.RunID, TaskID: taskID, AttemptID: *attempt, JobID: *job,
		EffectID: *effect, Nonce: *nonce, Kind: *kind, PayloadPath: "payloads/" + name, PayloadHash: hex.EncodeToString(sum[:])}
	raw, err := json.Marshal(env)
	if err != nil {
		return fail(err)
	}
	// The same validation the controller applies, so a malformed call fails here
	// instead of becoming a quarantined message.
	if _, err := controller.ParseEnvelope(raw); err != nil {
		fmt.Fprintln(os.Stderr, "tandem build event:", err)
		return exitcode.Usage
	}
	// Payload first, then the envelope that names it: the queue never holds an
	// envelope whose payload is missing.
	if err := writeAtomic(filepath.Join(dir, "payloads"), name, body); err != nil {
		return fail(err)
	}
	if err := writeAtomic(filepath.Join(dir, "q"), name, append(raw, '\n')); err != nil {
		return fail(err)
	}
	return printJSON(map[string]string{"event_id": eventID, "queue_file": filepath.Join(dir, "q", name), "payload": filepath.Join(dir, "payloads", name)})
}

// readRegular reads a regular, non-symlink file of at most max bytes.
func readRegular(path string, max int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	if info.Size() > max {
		return nil, fmt.Errorf("%s is %d bytes, over the limit of %d", path, info.Size(), max)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("%s grew past %d bytes while being read", path, max)
	}
	return b, nil
}

func writeAtomic(dir, name string, body []byte) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(dir, "."+name+".tmp")
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, name))
}

type ingestOut struct {
	Disposition string `json:"disposition"`
	EventID     string `json:"event_id,omitempty"`
	Version     int64  `json:"version,omitempty"`
	Duplicate   bool   `json:"duplicate,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

func cmdEventIngest(args []string) int {
	fs := flag.NewFlagSet("build event ingest", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	root := fs.String("root", "", "repository root")
	claimed := fs.String("claimed", "", "the claimed queue file")
	home := fs.String("state-home", os.Getenv("TANDEM_STATE_HOME"), "state home")
	settled := fs.Bool("session-settled", false, "the builder's session has finished its turn")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *root == "" || *claimed == "" {
		fmt.Fprintln(os.Stderr, eventUse)
		return exitcode.Usage
	}
	relay := paths.Relay(*root)
	run, err := controller.ReadActiveRun(relay)
	if err != nil {
		return fail(err)
	}
	if run == nil {
		return fail(fmt.Errorf("no durable run in %s", relay))
	}
	// A submission is applied only once the builder's turn has ended. An
	// unsettled one is retried later, not quarantined: nothing is wrong with it.
	if peekKind(*claimed) == workflow.EventBuilderSubmitted && !*settled {
		fmt.Fprintln(os.Stderr, "tandem build event: the builder session has not settled; retry the submission when it has")
		return exitcode.Internal
	}
	// The run's own controller decides, as a tick would: it captures a
	// submission's evidence before applying it, and judges the builder settled
	// through the run's transport (Controller.IngestClaimed).
	d, err := openDurable(*root, *home, false)
	if err != nil {
		return fail(err)
	}
	defer d.close()
	ctx := context.Background()
	ag, err := controller.RunAgents(ctx, d.st, d.art, d.run.RunID)
	if err != nil {
		return fail(err)
	}
	tr, err := durableTransport(runTransportSpec(ag, *root, d.art))
	if err != nil {
		return fail(err)
	}
	c, err := d.controller(tr)
	if err != nil {
		return fail(err)
	}
	res, err := c.IngestClaimed(ctx, d.run.RunID, *claimed)
	if err != nil {
		return fail(err)
	}
	return printJSON(ingestOut{Disposition: string(res.Disposition), EventID: res.EventID, Version: res.Receipt.Version,
		Duplicate: res.Receipt.Duplicate, Reason: res.Reason})
}

// peekKind reads the kind of a claimed file without trusting its shape.
func peekKind(path string) string {
	raw, err := readRegular(path, controller.MaxEnvelopeBytes)
	if err != nil {
		return ""
	}
	var k struct {
		Kind string `json:"kind"`
	}
	if json.Unmarshal(raw, &k) != nil {
		return ""
	}
	return k.Kind
}

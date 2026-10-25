package main

// tandem collaboration: the identity above runs that usage and budgets are
// accounted to.
//
// 	tandem collaboration create --policy FILE [--id ID] [--root DIR] [--state-home DIR]
// 	tandem collaboration register-source --collaboration ID --source FILE [--root DIR] [--state-home DIR]
//
// create stores the policy file as an artifact and prints the collaboration's
// id. The policy is a JSON object. Its optional input_ceilings,
// output_ceilings, input_reservations and output_reservations map a provider to
// a non-negative token count; enforce_budgets makes every launch pass dispatch
// admission; unlimited_providers lists providers declared without a ceiling.
// Other fields are kept as written. register-source records an expected source
// before it is launched. A source is only what its file says. Bad arguments
// exit 2; an unusable file or a store refusal exits 1 with the reason.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"regexp"
	"strconv"

	"github.com/emsnoel/tandem/internal/artifact"
	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/vcs"
	"github.com/emsnoel/tandem/internal/workflow"
)

const collaborationUse = `usage: tandem collaboration create --policy FILE [--id ID] [--root DIR] [--state-home DIR]
       tandem collaboration register-source --collaboration ID --source FILE [--root DIR] [--state-home DIR]
       tandem collaboration collect|close|report --collaboration ID [--json] [--sessions-dir DIR] [--root DIR] [--state-home DIR]
       tandem collaboration stop|resume --collaboration ID --actor NAME --reason TEXT [--root DIR] [--state-home DIR]
       tandem collaboration amend --collaboration ID --provider P [--input-ceiling N] [--output-ceiling N] --actor NAME --reason TEXT [--root DIR]`

var collabID = regexp.MustCompile(`^[0-9a-f]{32}$`)

func cmdCollaboration(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, collaborationUse)
		return exitcode.Usage
	}
	switch args[0] {
	case "create":
		return cmdCollaborationCreate(args[1:])
	case "register-source":
		return cmdCollaborationRegister(args[1:])
	case "collect", "close", "report":
		return cmdCollaborationUsage(args[0], args[1:])
	case "stop":
		return cmdCollaborationStop(args[1:])
	case "resume":
		return cmdCollaborationResume(args[1:])
	case "amend":
		return cmdCollaborationAmend(args[1:])
	}
	fmt.Fprintln(os.Stderr, collaborationUse)
	return exitcode.Usage
}

func collabFail(err error) int {
	fmt.Fprintln(os.Stderr, "tandem collaboration:", err)
	return exitcode.Internal
}

// inputError reports a policy or source file that cannot be used.
func inputError(format string, args ...any) int {
	fmt.Fprintf(os.Stderr, "tandem collaboration: "+format+"\n", args...)
	return exitcode.Internal
}

// openCheckoutState opens (creating if needed) the state of the checkout at
// root, without needing a run.
func openCheckoutState(f durableFlags) (*state.Store, *artifact.Store, error) {
	if f.root == "" {
		wd, _ := os.Getwd()
		top, err := vcs.Toplevel(wd)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: not inside a git checkout; pass --root", workflow.ErrInvalidTransition)
		}
		f.root = top
	}
	id, err := state.ResolveIdentity(f.root, f.home)
	if err != nil {
		return nil, nil, err
	}
	st, err := state.Open(context.Background(), id.StateRoot)
	if err != nil {
		return nil, nil, err
	}
	art, err := artifact.Open(id.StateRoot)
	if err != nil {
		st.Close()
		return nil, nil, err
	}
	return st, art, nil
}

func randomCollabID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// decodeStrict reads one JSON value with numbers kept exact and refuses NaN,
// Infinity and trailing data.
func decodeStrict(raw []byte, v any, disallowUnknown bool) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if disallowUnknown {
		dec.DisallowUnknownFields()
	}
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("trailing data after the JSON value")
	}
	return nil
}

func ceilings(raw any, field string) (map[string]int64, error) {
	if raw == nil {
		return nil, nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s must map a provider to a token ceiling", field)
	}
	out := map[string]int64{}
	for p, v := range m {
		n, ok := v.(json.Number)
		i, err := strconv.ParseInt(n.String(), 10, 64)
		if !ok || err != nil || i < 0 {
			return nil, fmt.Errorf("%s.%s must be a non-negative integer, not %v", field, p, v)
		}
		out[p] = i
	}
	return out, nil
}

func cmdCollaborationCreate(args []string) int {
	fs := flag.NewFlagSet("collaboration create", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var f durableFlags
	f.register(fs)
	policyPath := fs.String("policy", "", "the collaboration's policy JSON")
	id := fs.String("id", "", "the collaboration id (32 hex characters); random when omitted")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *policyPath == "" {
		fmt.Fprintln(os.Stderr, collaborationUse)
		return exitcode.Usage
	}
	if *id != "" && !collabID.MatchString(*id) {
		fmt.Fprintln(os.Stderr, "tandem collaboration: --id must be 32 lowercase hex characters")
		return exitcode.Usage
	}
	raw, err := os.ReadFile(*policyPath)
	if err != nil {
		return inputError("%v", err)
	}
	var policy map[string]any
	if err := decodeStrict(raw, &policy, false); err != nil || policy == nil {
		return inputError("the policy must be one JSON object: %v", err)
	}
	in, err := ceilings(policy["input_ceilings"], "input_ceilings")
	if err != nil {
		return inputError("%v", err)
	}
	out, err := ceilings(policy["output_ceilings"], "output_ceilings")
	if err != nil {
		return inputError("%v", err)
	}
	inRes, err := ceilings(policy["input_reservations"], "input_reservations")
	if err != nil {
		return inputError("%v", err)
	}
	outRes, err := ceilings(policy["output_reservations"], "output_reservations")
	if err != nil {
		return inputError("%v", err)
	}
	enforce, ok := policy["enforce_budgets"].(bool)
	if _, present := policy["enforce_budgets"]; present && !ok {
		return inputError("enforce_budgets must be true or false")
	}
	var unlimited []string
	if raw, present := policy["unlimited_providers"]; present {
		list, ok := raw.([]any)
		for _, p := range list {
			name, isText := p.(string)
			ok = ok && isText && name != ""
			unlimited = append(unlimited, name)
		}
		if !ok {
			return inputError("unlimited_providers must be a list of provider names")
		}
	}
	if *id == "" {
		if *id, err = randomCollabID(); err != nil {
			return collabFail(err)
		}
	}
	st, art, err := openCheckoutState(f)
	if err != nil {
		return collabFail(err)
	}
	defer st.Close()
	ctx := context.Background()
	hash, err := art.Put(ctx, bytes.NewReader(raw))
	if err != nil {
		return collabFail(err)
	}
	if err := st.AdoptArtifact(ctx, hash); err != nil {
		return collabFail(err)
	}
	if err := st.CreateCollaboration(ctx, state.CollaborationRequest{ID: *id, PolicyHash: hash, InputCeilings: in, OutputCeilings: out,
		InputReservations: inRes, OutputReservations: outRes, EnforceBudgets: enforce, Unlimited: unlimited}); err != nil {
		return collabFail(err)
	}
	return printJSON(map[string]string{"collaboration_id": *id, "policy_hash": hash})
}

// sourceFile is the register-source JSON: a state.SourceRequest whose
// collaboration comes from --collaboration. An omitted id is generated.
type sourceFile struct {
	ID             string `json:"id"`
	Provider       string `json:"provider"`
	Model          string `json:"model"`
	Role           string `json:"role"`
	SessionID      string `json:"session_id"`
	ParentSourceID string `json:"parent_source_id"`
	RunID          string `json:"run_id"`
	AttemptID      string `json:"attempt_id"`
	JobID          string `json:"job_id"`
	StartBoundary  string `json:"start_boundary"`
}

func cmdCollaborationRegister(args []string) int {
	fs := flag.NewFlagSet("collaboration register-source", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var f durableFlags
	f.register(fs)
	collab := fs.String("collaboration", "", "the collaboration id")
	sourcePath := fs.String("source", "", "the source JSON")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *collab == "" || *sourcePath == "" {
		fmt.Fprintln(os.Stderr, collaborationUse)
		return exitcode.Usage
	}
	raw, err := os.ReadFile(*sourcePath)
	if err != nil {
		return inputError("%v", err)
	}
	var src sourceFile
	if err := decodeStrict(raw, &src, true); err != nil {
		return inputError("the source must be one JSON object of known fields: %v", err)
	}
	if src.ID == "" {
		if src.ID, err = randomCollabID(); err != nil {
			return collabFail(err)
		}
	}
	st, _, err := openCheckoutState(f)
	if err != nil {
		return collabFail(err)
	}
	defer st.Close()
	err = st.RegisterSource(context.Background(), state.SourceRequest{ID: src.ID, CollaborationID: *collab, Provider: src.Provider,
		Model: src.Model, Role: src.Role, SessionID: src.SessionID, ParentSourceID: src.ParentSourceID, RunID: src.RunID,
		AttemptID: src.AttemptID, JobID: src.JobID, StartBoundary: src.StartBoundary})
	if err != nil {
		return collabFail(err)
	}
	return printJSON(map[string]string{"source_id": src.ID, "collaboration_id": *collab})
}

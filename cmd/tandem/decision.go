package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/emsnoel/tandem/internal/agent"
	"github.com/emsnoel/tandem/internal/decision"
	"github.com/emsnoel/tandem/internal/evidence"
	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/paths"
)

// askTimeout bounds the call; a batched call takes about 0.7s.
const askTimeout = 2 * time.Second

// apiKey resolves the credential from the environment only. A repository
// manifest is cloned from elsewhere, so it must never supply one. A missing
// key means the feature is off.
func apiKey() string { return os.Getenv("TYPESAFE_API_KEY") }

// builderIdentity reads the builder's kind and autonomy from the relay record,
// or empty strings when it is missing or incomplete.
func builderIdentity(root string) (kind, autonomy string) {
	b, err := os.ReadFile(filepath.Join(paths.Relay(root), "builder.json"))
	if err != nil {
		return "", ""
	}
	var rec builderRecord
	if json.Unmarshal(b, &rec) != nil || rec.Kind == "" || rec.Autonomy == "" {
		// Both fields or neither: a half record is not a usable identity.
		return "", ""
	}
	return rec.Kind, rec.Autonomy
}

func cmdDecision(args []string) int {
	if len(args) == 0 || args[0] != "assess" {
		fmt.Fprintln(os.Stderr, "usage: tandem decision assess --plan P --root R [--render] [--dry-run]")
		return exitcode.Usage
	}

	fs := flag.NewFlagSet("decision assess", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	plan := fs.String("plan", "", "path to the plan under consideration")
	root := fs.String("root", "", "repository root")
	hasCheck := fs.Bool("has-check-command", false, "the project declares a check_command")
	render := fs.Bool("render", false, "print the advisory block instead of JSON")
	dryRun := fs.Bool("dry-run", false, "print the payload that would be sent and exit")
	statusFile := fs.String("status", "", "write the outcome (off, advice, no_advice, error) to this file")
	if err := fs.Parse(args[1:]); err != nil {
		return exitcode.Usage
	}
	if *plan == "" || *root == "" {
		fmt.Fprintln(os.Stderr, "tandem decision: --plan and --root are required")
		return exitcode.Usage
	}

	text, err := os.ReadFile(*plan)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tandem decision: %v\n", err)
		return exitcode.Internal
	}

	// The builder identity is read here because bin/tandem-handoff has no
	// BUILDER_KIND in scope. A missing record leaves kind empty, and the renderer
	// omits any floor it cannot confirm.
	kind, autonomy := builderIdentity(*root)

	snap := evidence.Build(string(text), *hasCheck, kind, autonomy,
		decision.ContractVersion, decision.PolicyVersion, decision.Model)

	if *dryRun {
		payload := map[string]any{
			"model":     decision.Model,
			"state":     snap,
			"questions": decision.Questions(),
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(payload); err != nil {
			return exitcode.Internal
		}
		return exitcode.OK
	}

	key := apiKey()
	if key == "" {
		// Off: silent with exit 0, so callers can always run this.
		writeStatus(*statusFile, "off", "")
		return exitcode.OK
	}

	ctx, cancel := context.WithTimeout(context.Background(), askTimeout)
	defer cancel()

	raw, err := newJevClient(key, askTimeout).Ask(ctx, decision.Model, snap, decision.Questions())
	if err != nil {
		// Advice is optional: report to stderr and leave stdout empty.
		fmt.Fprintf(os.Stderr, "tandem decision: %v\n", err)
		writeStatus(*statusFile, "error", err.Error())
		return exitcode.OK
	}

	// The transport hands back what came off the wire; the staffing meaning is
	// attached here. A malformed batch is an error, not a zero value.
	answers, err := decision.ParseStaffing(raw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tandem decision: %v\n", err)
		writeStatus(*statusFile, "error", err.Error())
		return exitcode.OK
	}

	adv := decision.Assess(answers)
	if adv.Outcome == decision.OutcomeNoAdvice {
		writeStatus(*statusFile, "no_advice", "")
	} else {
		writeStatus(*statusFile, "advice", "")
	}

	if *render {
		effortExpressible := false
		if a, err := agent.Get(kind); err == nil {
			effortExpressible = len(a.Supports("subagent_effort")) > 0
		}
		fmt.Print(adv.Render(effortExpressible))
		return exitcode.OK
	}

	out := struct {
		decision.Advisory
		Digest   string `json:"digest"`
		Contract string `json:"contract"`
		Policy   string `json:"policy"`
		ModelID  string `json:"model"`
	}{adv, snap.Digest, decision.ContractVersion, decision.PolicyVersion, decision.Model}

	enc := json.NewEncoder(os.Stdout)
	if err := enc.Encode(out); err != nil {
		return exitcode.Internal
	}
	return exitcode.OK
}

// writeStatus records how an assessment ended, for the human. Every outcome but
// "advice" leaves the prompt identical to "off", so this tells "asked, nothing
// to say" from "never asked". A token on line 1, the reason on line 2. Best
// effort.
func writeStatus(path, token, reason string) {
	if path == "" {
		return
	}
	s := token + "\n"
	if reason != "" {
		s += strings.ReplaceAll(reason, "\n", " ") + "\n"
	}
	_ = os.WriteFile(path, []byte(s), 0o600)
}

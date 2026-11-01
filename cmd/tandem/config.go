package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/emsnoel/tandem/internal/agent"
	"github.com/emsnoel/tandem/internal/config"
	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/paths"
)

// loaded is what both subcommands need after flag parsing and config merging.
// Exactly one of Resolved and Reviewer is meaningful: Reviewer is set for
// --role reviewer, whose resolution can also say "none" or "invalid".
type loaded struct {
	Resolved config.Resolved
	Reviewer *config.ReviewerResolution
	Cwd      string
	Root     string
	Warnings []string
}

// loadResolved is the shared front half of both subcommands: parse the common
// flags, read both config sources, and merge them.
func loadResolved(fs *flag.FlagSet, args []string) (loaded, int) {
	role := fs.String("role", "builder", "planner|builder|reviewer")
	root := fs.String("root", ".", "repository root holding the manifest")
	cwd := fs.String("cwd", "", "working directory the agent should start in")
	var ov config.Overrides
	fs.StringVar(&ov.Kind, "kind", "", "override the agent kind")
	fs.StringVar(&ov.Model, "model", "", "override the model")
	fs.StringVar(&ov.Effort, "effort", "", "override the effort")
	fs.StringVar(&ov.Autonomy, "autonomy", "", "override the autonomy")
	if err := fs.Parse(args); err != nil {
		return loaded{}, exitcode.Usage
	}
	if *role != "planner" && *role != "builder" && *role != "reviewer" {
		fmt.Fprintf(os.Stderr, "tandem: --role must be planner, builder or reviewer, got %q\n", *role)
		return loaded{}, exitcode.Usage
	}
	absRoot, err := filepath.Abs(*root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tandem: %v\n", err)
		return loaded{}, exitcode.Internal
	}

	repo, warns, err := config.LoadRepo(paths.Manifest(absRoot))
	if err != nil {
		fmt.Fprintf(os.Stderr, "tandem: %v\n", err)
		return loaded{}, exitcode.Internal
	}
	user, err := config.LoadUser(config.UserConfigPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "tandem: %v\n", err)
		return loaded{}, exitcode.Internal
	}

	l := loaded{Cwd: *cwd, Root: absRoot, Warnings: warns}
	if agent.Role(*role) == agent.RoleReviewer {
		res := config.ResolveReviewer(repo, user, os.Getenv, ov)
		if res.Resolved != nil {
			res.Resolved.Warnings = append(append([]string{}, warns...), res.Resolved.Warnings...)
		}
		l.Reviewer = &res
		return l, exitcode.OK
	}
	r := config.ResolveRole(agent.Role(*role), repo, user, os.Getenv, ov)
	r.Warnings = append(warns, r.Warnings...)
	l.Resolved = r
	return l, exitcode.OK
}

func cmdConfig(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: tandem config <resolve|approve-kind|record-builder|check-command|project|builder-kind> ...")
		return exitcode.Usage
	}
	if args[0] == "approve-kind" {
		return cmdApprove(args[1:])
	}
	if args[0] == "record-builder" {
		return cmdRecordBuilder(args[1:])
	}
	if args[0] == "check-command" {
		return cmdConfigCheckCommand(args[1:])
	}
	if args[0] == "project" {
		return cmdConfigProject(args[1:])
	}
	if args[0] == "builder-kind" {
		return cmdConfigBuilderKind(args[1:])
	}
	if args[0] != "resolve" {
		fmt.Fprintln(os.Stderr, "usage: tandem config <resolve|approve-kind|record-builder|check-command|project|builder-kind> ...")
		return exitcode.Usage
	}
	fs := flag.NewFlagSet("config resolve", flag.ContinueOnError)
	adopted := fs.String("adopted", "", "running kind of an adopted agent")
	asFields := fs.Bool("fields", false, "print NUL-separated fields for bash (resolveFields) in place of JSON")
	l, rc := loadResolved(fs, args[1:])
	if rc != exitcode.OK {
		return rc
	}
	if l.Reviewer != nil {
		if *asFields {
			writeResolveFields(os.Stdout, string(l.Reviewer.Status), l.Reviewer.Reason, l.Reviewer.Resolved)
			return exitcode.OK
		}
		return emitJSON(l.Reviewer)
	}
	r := l.Resolved
	if *adopted != "" {
		r = r.MarkAdopted(*adopted)
	}
	if *asFields {
		writeResolveFields(os.Stdout, string(config.ReviewerResolved), "", &r)
		return exitcode.OK
	}
	return emitJSON(r)
}

// writeResolveFields prints a resolution as eleven NUL fields: status, reason,
// kind, model, effort, autonomy, the four values' sources in that order, and
// the unenforced axes joined by spaces. r is nil for a reviewer that is none
// or invalid, which leaves the last nine fields empty.
func writeResolveFields(w io.Writer, status, reason string, r *config.Resolved) {
	if r == nil {
		r = &config.Resolved{}
	}
	writeFields(w, status, reason,
		r.Kind.Value, r.Model.Value, r.Effort.Value, r.Autonomy.Value,
		string(r.Kind.From), string(r.Model.From), string(r.Effort.From), string(r.Autonomy.From),
		strings.Join(r.Unenforced, " "))
}

func emitJSON(v any) int {
	b, err := json.Marshal(v)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tandem: %v\n", err)
		return exitcode.Internal
	}
	fmt.Fprintln(os.Stdout, string(b))
	return exitcode.OK
}

package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/emsnoel/tandem/internal/config"
	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/paths"
)

const reviewerUse = "usage: tandem reviewer show [--root DIR | --relay DIR] [--json] | tandem reviewer record ..."

// cmdReviewer prints the reviewer pane tandem-pair recorded, through
// config.LoadReviewer, so no script parses reviewer.json itself. --relay names
// the relay the script already resolved.
//
// By default it prints kind, name, model_measured and model, tab-separated.
// --json prints the whole record. It exits exitcode.NoReviewer with no record
// and exitcode.ReviewerIncomplete when the record is unreadable or incomplete.
func cmdReviewer(args []string) int {
	if len(args) > 0 && args[0] == "record" {
		return cmdReviewerRecord(args[1:])
	}
	if len(args) == 0 || args[0] != "show" {
		fmt.Fprintln(os.Stderr, reviewerUse)
		return exitcode.Usage
	}
	fs := flag.NewFlagSet("reviewer show", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	root := fs.String("root", "", "repository root (default: the current directory)")
	relay := fs.String("relay", "", "the relay directory itself, for a caller that has already resolved it")
	asJSON := fs.Bool("json", false, "print the whole record as JSON")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 || (*root != "" && *relay != "") {
		fmt.Fprintln(os.Stderr, reviewerUse)
		return exitcode.Usage
	}
	dir := *relay
	if dir == "" {
		absRoot, err := filepath.Abs(*root)
		if err != nil {
			fmt.Fprintf(os.Stderr, "tandem: %v\n", err)
			return exitcode.Internal
		}
		dir = paths.Relay(absRoot)
	}
	rec, err := config.LoadReviewer(dir)
	switch {
	case errors.Is(err, config.ErrNoReviewer):
		return exitcode.NoReviewer
	case err != nil:
		return exitcode.ReviewerIncomplete
	case *asJSON:
		if err := json.NewEncoder(os.Stdout).Encode(rec); err != nil {
			return fail(err)
		}
	default:
		fmt.Printf("%s\t%s\t%t\t%s\n", rec.Kind, rec.Name, rec.ModelMeasured, rec.Model)
	}
	return exitcode.OK
}

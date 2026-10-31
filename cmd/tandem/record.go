package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/emsnoel/tandem/internal/config"
	"github.com/emsnoel/tandem/internal/exitcode"
)

// cmdRecordBuilder resolves the builder as `config resolve` does and writes
// builder.json in --relay, unless --dry-run. It prints kind, autonomy and the
// record's JSON as NUL fields. An empty kind or autonomy exits
// BuilderUnresolved with nothing printed.
func cmdRecordBuilder(args []string) int {
	fs := flag.NewFlagSet("config record-builder", flag.ContinueOnError)
	relay := fs.String("relay", "", "the relay directory to write builder.json in")
	adopted := fs.String("adopted", "", "running kind of an adopted builder")
	dryRun := fs.Bool("dry-run", false, "print the record without writing it")
	l, rc := loadResolved(fs, append([]string{"--role", "builder"}, args...))
	if rc != exitcode.OK {
		return rc
	}
	if *relay == "" && !*dryRun {
		fmt.Fprintln(os.Stderr, "usage: tandem config record-builder --relay DIR [resolve flags] [--adopted KIND] [--dry-run]")
		return exitcode.Usage
	}
	r := l.Resolved
	if *adopted != "" {
		r = r.MarkAdopted(*adopted)
	}
	rec := config.BuilderRecord{Kind: r.Kind.Value, Autonomy: r.Autonomy.Value, Adopted: *adopted != ""}
	if strings.TrimSpace(rec.Kind) == "" || strings.TrimSpace(rec.Autonomy) == "" {
		return exitcode.BuilderUnresolved
	}
	return printRecord(rec, *dryRun, func() error { return config.WriteBuilder(*relay, rec) }, rec.Kind, rec.Autonomy)
}

// cmdReviewerRecord writes reviewer.json for the reviewer pane bin/tandem-pair
// started (--started) or found running, keeping a measured model only as
// config.NextReviewer allows. It prints the recorded model and the record's
// JSON as NUL fields.
func cmdReviewerRecord(args []string) int {
	fs := flag.NewFlagSet("reviewer record", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	relay := fs.String("relay", "", "the relay directory to write reviewer.json in")
	kind := fs.String("kind", "", "the reviewer's kind")
	name := fs.String("name", "", "the reviewer's agent name")
	model := fs.String("model", "", "the model a started reviewer was launched with")
	autonomy := fs.String("autonomy", "", "the reviewer's resolved autonomy")
	started := fs.Bool("started", false, "tandem-pair started this reviewer")
	dryRun := fs.Bool("dry-run", false, "print the record without writing it")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *relay == "" || *kind == "" || *name == "" {
		fmt.Fprintln(os.Stderr, "usage: tandem reviewer record --relay DIR --kind K --name N --autonomy A [--model M] [--started] [--dry-run]")
		return exitcode.Usage
	}
	var prev *config.ReviewerRecord
	if !*started {
		if p, err := config.LoadReviewer(*relay); err == nil {
			prev = &p
		}
	}
	rec := config.NextReviewer(prev, *kind, *name, *model, *autonomy, *started)
	return printRecord(rec, *dryRun, func() error { return config.WriteReviewer(*relay, rec) }, rec.Model)
}

// printRecord writes the record unless dryRun, then prints lead and the
// record's JSON as NUL fields.
func printRecord(rec any, dryRun bool, write func() error, lead ...string) int {
	b, err := json.Marshal(rec)
	if err != nil {
		return fail(err)
	}
	if !dryRun {
		if err := write(); err != nil {
			return fail(err)
		}
	}
	writeFields(os.Stdout, append(lead, string(b))...)
	return exitcode.OK
}

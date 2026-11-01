package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/emsnoel/tandem/internal/exitcode"
)

const gateRecordUse = "usage: tandem gate record --relay DIR --id ID --plan PATH --verdict clean|objections " +
	"--kind K --autonomy A --reviewer-kind K --reviewer-model M --reviewer-measured=true|false"

// cmdGateRecord writes the review sidecar for bin/tandem-verdict, the record
// `gate review` binds a review to. The script has already read and checked
// the builder and reviewer records, so it passes their values in, and the
// sidecar holds exactly what it checked.
func cmdGateRecord(args []string) int {
	fs := flag.NewFlagSet("gate record", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	str := func(n string) *string { return fs.String(n, "", n) }
	relay, id, plan, verdict := str("relay"), str("id"), str("plan"), str("verdict")
	kind, autonomy, rkind, rmodel := str("kind"), str("autonomy"), str("reviewer-kind"), str("reviewer-model")
	measured := fs.Bool("reviewer-measured", false, "whether the reviewer's model was measured")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *relay == "" || !reviewIDPattern.MatchString(*id) ||
		(*verdict != "clean" && *verdict != "objections") || *plan == "" || *kind == "" || *autonomy == "" {
		fmt.Fprintln(os.Stderr, gateRecordUse)
		return exitcode.Usage
	}
	meta := reviewMeta{Plan: *plan, Verdict: *verdict, Kind: *kind, Autonomy: *autonomy, ReviewedBy: "reviewer",
		ReviewerKind: *rkind, ReviewerModel: *rmodel, ReviewerModelMeasured: *measured}
	if err := writeFileAtomic(filepath.Join(*relay, "reviews", *id+".json"), meta); err != nil {
		fmt.Fprintln(os.Stderr, "tandem gate record:", err)
		return exitcode.Internal
	}
	return exitcode.OK
}

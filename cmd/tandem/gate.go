package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/emsnoel/tandem/internal/controller"
	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/gate"
	"github.com/emsnoel/tandem/internal/mailbox"
	"github.com/emsnoel/tandem/internal/vcs"
)

// snapshotFile is the pre-check state carried from --phase pre to --phase
// post. It is a file so bash never parses it.
type snapshotFile struct {
	Reports      mailbox.Snapshot `json:"reports"`
	Tree         string           `json:"tree"`
	TreeMeasured bool             `json:"tree_measured"`
	Status       string           `json:"status"`
}

func cmdGate(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: tandem gate <teardown|review|scope|latest-plan> ...")
		return exitcode.Usage
	}
	if args[0] == "review" {
		return cmdGateReview(args[1:])
	}
	if args[0] == "scope" {
		return cmdGateScope(args[1:])
	}
	if args[0] == "latest-plan" {
		return cmdGateLatestPlan(args[1:])
	}
	if args[0] != "teardown" {
		fmt.Fprintln(os.Stderr, "usage: tandem gate <teardown|review|scope|latest-plan> ...")
		return exitcode.Usage
	}

	fs := flag.NewFlagSet("gate teardown", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	// May be empty, meaning no builder pane is left and every gate is skipped.
	builderPane := fs.String("builder-pane", "", "pane hosting the builder; empty means none")
	phase := fs.String("phase", "", "pre or post")
	mailboxDir := fs.String("mailbox", "", "relay mailbox directory")
	root := fs.String("root", "", "repository root")
	status := fs.String("status", "", "builder status read before the check")
	statusNow := fs.String("status-now", "", "builder status re-read after the check")
	snapPath := fs.String("snapshot", "", "file carrying pre-check state to --phase post")
	force := fs.Bool("force", false, "bypass every gate")
	skipChecks := fs.Bool("skip-checks", false, "the caller declared no check will run")
	hasCheck := fs.Bool("has-check-command", false, "the project declares a check_command")
	var verified stringList
	fs.Var(&verified, "verified", "handback id signed off; repeat for more than one")
	var checkPaths stringList
	fs.Var(&checkPaths, "check-paths", "pathspec (relative to --root) to scope the tree fingerprint to; repeat for more than one; default scopes to the whole tree")
	if err := fs.Parse(args[1:]); err != nil {
		return exitcode.Usage
	}
	if *phase != "pre" && *phase != "post" {
		fmt.Fprintln(os.Stderr, "tandem gate teardown: --phase must be pre or post")
		return exitcode.Usage
	}

	in := gate.Input{
		BuilderPane:     *builderPane,
		Status:          *status,
		StatusNow:       *statusNow,
		HasCheckCommand: *hasCheck,
		SkipChecks:      *skipChecks,
		Force:           *force,
	}

	reportsDir := filepath.Join(*mailboxDir, "reports")

	// The snapshot must live outside git's view, or the tree fingerprint would
	// change between phases. The mailbox is already ignored, so default there.
	if *snapPath == "" && *mailboxDir != "" {
		*snapPath = filepath.Join(*mailboxDir, ".teardown-snapshot")
	}

	switch *phase {
	case "pre":
		ok, pending, err := partitionReports(reportsDir, verified)
		if err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			return exitcode.Internal
		}
		in.ReportsOK, in.ReportsPending = ok, pending
		// A durable run is signed off on its own evidence. Only read when a
		// gate would use it: a pane already gone, or --force, skips them all.
		if *builderPane != "" && !*force && *mailboxDir != "" {
			d, err := durableTeardownEvidence(*mailboxDir)
			if err != nil {
				fmt.Fprintln(os.Stderr, "tandem gate teardown: reading the durable run's evidence:", err)
				return exitcode.Internal
			}
			in.Durable = d
		}

		v := gate.Pre(in)
		emit(v)
		if v.Code != 0 {
			return v.Code
		}
		// Written last and only on allow.
		if *snapPath != "" {
			if err := writeSnapshot(*snapPath, reportsDir, *root, *status, checkPaths); err != nil {
				fmt.Fprintln(os.Stderr, err.Error())
				return exitcode.Internal
			}
		}
		return exitcode.OK

	default: // post
		before, err := readSnapshot(*snapPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			return exitcode.Internal
		}
		after, err := mailbox.Take(reportsDir)
		if err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			return exitcode.Internal
		}
		in.ReportsBefore, in.ReportsAfter = before.Reports, after
		in.TreeBefore, in.TreeMeasured = before.Tree, before.TreeMeasured
		if before.TreeMeasured {
			treeAfter, err := vcs.Fingerprint(*root, checkPaths)
			if err != nil {
				// Measurable before but not now: treat as unmeasured rather than as a change.
				in.TreeMeasured = false
			} else {
				in.TreeAfter = treeAfter
			}
		}
		v := gate.Post(in)
		emit(v)
		return v.Code
	}
}

// durableTeardownEvidence reads the evidence of the durable run in the relay,
// or returns nil when the relay holds none. The relay sits at the root of its
// checkout, which is where the run's state is found; the state home is
// TANDEM_STATE_HOME or the default, as for every durable command.
func durableTeardownEvidence(relay string) (*gate.DurableRun, error) {
	if ar, err := controller.ReadActiveRun(relay); err != nil || ar == nil {
		return nil, err
	}
	d, err := openDurable(filepath.Dir(relay), os.Getenv("TANDEM_STATE_HOME"), true)
	if err != nil {
		return nil, err
	}
	defer d.close()
	c, err := d.controller(noSend{})
	if err != nil {
		return nil, err
	}
	ev, err := c.TeardownEvidence(context.Background(), d.run.RunID)
	if err != nil {
		return nil, err
	}
	out := &gate.DurableRun{RunID: ev.RunID, State: ev.State}
	for _, t := range ev.Tasks {
		out.Tasks = append(out.Tasks, gate.DurableTask{ID: t.ID, Unmet: t.Unmet})
	}
	return out, nil
}

// emit prints a verdict's detail, one item per line, for bash to read into an
// array and interpolate into the prose it already prints.
func emit(v gate.Verdict) {
	for _, d := range v.Detail {
		fmt.Println(d)
	}
}

// partitionReports splits the handback reports into signed-off and pending.
// A report is signed off when its id was passed with --verified or a sibling
// <id>.verified marker exists.
func partitionReports(reportsDir string, verified []string) (ok, pending []string, err error) {
	snap, err := mailbox.Take(reportsDir)
	if err != nil {
		return nil, nil, err
	}
	signed := map[string]bool{}
	for _, v := range verified {
		signed[v] = true
	}
	for id := range snap {
		if signed[id] {
			ok = append(ok, id)
			continue
		}
		if _, statErr := os.Stat(filepath.Join(reportsDir, id+".verified")); statErr == nil {
			ok = append(ok, id)
			continue
		}
		pending = append(pending, id)
	}
	sortStrings(ok)
	sortStrings(pending)
	return ok, pending, nil
}

// sortStrings is an insertion sort for short id lists.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func writeSnapshot(path, reportsDir, root, status string, checkPaths []string) error {
	reports, err := mailbox.Take(reportsDir)
	if err != nil {
		return err
	}
	sf := snapshotFile{Reports: reports, Status: status}
	if tree, err := vcs.Fingerprint(root, checkPaths); err == nil {
		sf.Tree, sf.TreeMeasured = tree, true
	}
	b, err := json.Marshal(sf)
	if err != nil {
		return err
	}
	// Write then rename, so --phase post can never read a half-written snapshot.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func readSnapshot(path string) (snapshotFile, error) {
	var sf snapshotFile
	if path == "" {
		return sf, fmt.Errorf("--phase post needs --snapshot")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return sf, err
	}
	return sf, json.Unmarshal(b, &sf)
}

package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/emsnoel/tandem/internal/agent"
	"github.com/emsnoel/tandem/internal/doctor"
	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/paths"
	"github.com/emsnoel/tandem/internal/vcs"
)

// doctorProbe is how doctor reaches herdr and the agent CLIs. Tests swap it.
var doctorProbe doctor.Probe = doctor.RealProbe{}

// doctorReport is the --json output. Text output prints the same fields.
type doctorReport struct {
	Root     string          `json:"root"`
	Manifest string          `json:"manifest"`
	Results  []doctor.Result `json:"results"`
	Failures int             `json:"failures"`
	Warnings int             `json:"warnings"`
}

// cmdDoctor checks this machine and repository for everything that would stop
// a pairing, and prints each problem with its fix. It writes nothing. Exit 30
// means at least one check failed.
func cmdDoctor(args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "print the results as JSON")
	cwd := fs.String("cwd", ".", "folder the agents will start in; the repo is its git toplevel")
	var flags doctor.RoleFlags
	fs.StringVar(&flags.Builder.Kind, "builder", "", "builder kind, as tandem-pair --builder")
	fs.StringVar(&flags.Builder.Model, "builder-model", "", "builder model")
	fs.StringVar(&flags.Builder.Effort, "builder-effort", "", "builder effort")
	fs.StringVar(&flags.Builder.Autonomy, "builder-autonomy", "", "builder autonomy")
	fs.StringVar(&flags.Reviewer.Kind, "reviewer", "", "reviewer kind, as tandem-pair --reviewer")
	fs.StringVar(&flags.Reviewer.Model, "reviewer-model", "", "reviewer model")
	fs.StringVar(&flags.Reviewer.Effort, "reviewer-effort", "", "reviewer effort")
	if err := fs.Parse(args); err != nil {
		return exitcode.Usage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: tandem doctor [--json] [--cwd DIR] [--builder KIND] [--reviewer KIND] ...")
		return exitcode.Usage
	}
	dir, err := filepath.Abs(*cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tandem: %v\n", err)
		return exitcode.Internal
	}
	// The same root tandem-pair uses: the git toplevel, or the folder itself.
	root := dir
	if top, err := vcs.Toplevel(dir); err == nil {
		root = top
	}

	rep := doctorReport{Root: root}
	roles, err := doctor.ResolveRoles(root, flags, os.Getenv)
	if err != nil {
		// Without a readable config, check the machine for the planner alone.
		rep.Results = doctor.Machine(doctorProbe, doctor.Uses{doctor.PlannerKind: {agent.RolePlanner}}, os.Getenv)
		rep.Results = append(rep.Results, doctor.Result{Group: "repo", Name: "config", Status: doctor.Fail,
			Detail: err.Error(), Fix: "fix the file named above, then run tandem doctor again"})
	} else {
		rep.Manifest = roles.Manifest
		rep.Results = append(doctor.Machine(doctorProbe, roles.Uses(), os.Getenv),
			doctor.Repo(root, dir, roles, os.Getenv)...)
	}
	rep.Failures, rep.Warnings = doctor.Count(rep.Results)

	if *asJSON {
		if rc := emitJSON(rep); rc != exitcode.OK {
			return rc
		}
	} else {
		printDoctor(os.Stdout, rep)
	}
	if rep.Failures > 0 {
		return exitcode.DoctorFailed
	}
	return exitcode.OK
}

func printDoctor(w io.Writer, rep doctorReport) {
	manifest := "no " + paths.ManifestName
	if rep.Manifest != "" {
		manifest = filepath.Base(rep.Manifest)
	}
	fmt.Fprintf(w, "tandem doctor  (repo: %s, config: %s)\n", tilde(rep.Root), manifest)
	group := ""
	for _, r := range rep.Results {
		if r.Group != group {
			group = r.Group
			fmt.Fprintf(w, "\n%s\n", strings.ToUpper(group[:1])+group[1:])
		}
		label := map[doctor.Status]string{doctor.OK: "ok", doctor.Warn: "warn", doctor.Fail: "FAIL"}[r.Status]
		writeWrapped(w, fmt.Sprintf("  %-4s  ", label), "        ", r.Detail)
		if r.Fix != "" {
			writeWrapped(w, "        fix: ", "             ", r.Fix)
		}
	}
	fmt.Fprintf(w, "\n%s\n", doctorSummary(rep.Failures, rep.Warnings))
}

// doctorWidth is the widest line doctor prints. Longer text wraps at spaces
// under its own indent; a single word longer than the width is left whole.
const doctorWidth = 100

func writeWrapped(w io.Writer, first, rest, text string) {
	line, prefix := "", first
	for _, word := range strings.Fields(text) {
		if line != "" && len(prefix)+len(line)+1+len(word) > doctorWidth {
			fmt.Fprintf(w, "%s%s\n", prefix, line)
			line, prefix = "", rest
		}
		if line != "" {
			line += " "
		}
		line += word
	}
	fmt.Fprintf(w, "%s%s\n", prefix, line)
}

func doctorSummary(failures, warnings int) string {
	if failures == 0 && warnings == 0 {
		return "No problems found."
	}
	return plural(failures, "failure") + ", " + plural(warnings, "warning") + "."
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// tilde shortens a path under the home directory for display.
func tilde(p string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" && strings.HasPrefix(p, home+string(filepath.Separator)) {
		return "~" + p[len(home):]
	}
	return p
}

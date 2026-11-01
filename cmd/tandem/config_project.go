package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/emsnoel/tandem/internal/config"
	"github.com/emsnoel/tandem/internal/exitcode"
)

// projectJSON is bin/tandem-project's output, keys in its order.
type projectJSON struct {
	Root             string   `json:"root"`
	PlansDir         string   `json:"plans_dir"`
	CheckCommand     string   `json:"check_command"`
	ConventionsFile  string   `json:"conventions_file"`
	BuilderKind      string   `json:"builder_kind"`
	PromptExtra      string   `json:"prompt_extra"`
	CheckPaths       []string `json:"check_paths"`
	TaskInputBudget  int64    `json:"task_input_budget"`
	ContextLimit     int64    `json:"context_limit"`
	BuildEngine      string   `json:"build_engine"`
	ReviewDelivery   string   `json:"review_delivery"`
	AcceptancePolicy string   `json:"acceptance_policy"`
	Collaboration    string   `json:"collaboration"`
	Source           string   `json:"source"`
}

// writeIndentedJSON writes v as jq prints it: two-space indent, and & < >
// left as they are rather than escaped.
func writeIndentedJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// resolveProjectFlags parses --root and --from and resolves the project.
func resolveProjectFlags(name string, args []string) (config.Project, bool, int) {
	fs := flag.NewFlagSet("config "+name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	root := fs.String("root", "", "the directory detection looks in, echoed as given")
	from := fs.String("from", "", "where the manifest search starts (default: --root)")
	asFields := fs.Bool("fields", false, "print NUL-separated fields for bash in place of JSON")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *root == "" {
		fmt.Fprintf(os.Stderr, "usage: tandem config %s --root DIR [--from DIR] [--fields]\n", name)
		return config.Project{}, false, exitcode.Usage
	}
	if *from == "" {
		*from = *root
	}
	manifest, err := nearestManifest(*from)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tandem: %v\n", err)
		return config.Project{}, false, exitcode.Internal
	}
	return config.ResolveProject(*root, manifest), *asFields, exitcode.OK
}

// cmdConfigProject answers "what are this project's settings" for
// bin/tandem-project and bin/tandem-handoff. With --fields it prints root,
// plans_dir, check_command, conventions_file, builder_kind, prompt_extra,
// task_input_budget, context_limit, build_engine, review_delivery,
// acceptance_policy, collaboration, source and invalid_manifest, then each
// check path, every one NUL-terminated.
func cmdConfigProject(args []string) int {
	p, asFields, rc := resolveProjectFlags("project", args)
	if rc != exitcode.OK {
		return rc
	}
	if asFields {
		writeFields(os.Stdout, append([]string{p.Root, p.PlansDir, p.CheckCommand, p.ConventionsFile,
			p.BuilderKind, p.PromptExtra, strconv.FormatInt(p.TaskInputBudget, 10),
			strconv.FormatInt(p.ContextLimit, 10), p.BuildEngine, p.ReviewDelivery, p.AcceptancePolicy,
			p.Collaboration, p.Source, p.InvalidManifest}, p.CheckPaths...)...)
		return exitcode.OK
	}
	out := projectJSON{p.Root, p.PlansDir, p.CheckCommand, p.ConventionsFile, p.BuilderKind, p.PromptExtra,
		p.CheckPaths, p.TaskInputBudget, p.ContextLimit, p.BuildEngine, p.ReviewDelivery, p.AcceptancePolicy,
		p.Collaboration, p.Source}
	if out.CheckPaths == nil {
		out.CheckPaths = []string{}
	}
	if err := writeIndentedJSON(os.Stdout, out); err != nil {
		fmt.Fprintf(os.Stderr, "tandem: %v\n", err)
		return exitcode.Internal
	}
	return exitcode.OK
}

package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/emsnoel/tandem/internal/exitcode"
)

// manifestJSON is the .tandem.json bin/tandem-init writes, keys in the order
// it always wrote them. prompt_extra starts empty for the user to fill in.
type manifestJSON struct {
	PlansDir        string `json:"plans_dir"`
	CheckCommand    string `json:"check_command"`
	ConventionsFile string `json:"conventions_file"`
	BuilderKind     string `json:"builder_kind"`
	PromptExtra     string `json:"prompt_extra"`
}

const writeManifestUse = "usage: tandem config write-manifest --file PATH [--plans-dir D] [--check-command C] " +
	"[--conventions-file F] [--builder-kind K]"

// cmdConfigWriteManifest writes a project manifest from the settings
// bin/tandem-init detected, through a temporary file and a rename.
func cmdConfigWriteManifest(args []string) int {
	fs := flag.NewFlagSet("config write-manifest", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	file := fs.String("file", "", "the manifest to write")
	var m manifestJSON
	fs.StringVar(&m.PlansDir, "plans-dir", "", "plans_dir")
	fs.StringVar(&m.CheckCommand, "check-command", "", "check_command")
	fs.StringVar(&m.ConventionsFile, "conventions-file", "", "conventions_file")
	fs.StringVar(&m.BuilderKind, "builder-kind", "", "builder_kind")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *file == "" {
		fmt.Fprintln(os.Stderr, writeManifestUse)
		return exitcode.Usage
	}
	if err := writeFileAtomic(*file, m); err != nil {
		fmt.Fprintln(os.Stderr, "tandem config write-manifest:", err)
		return exitcode.Internal
	}
	return exitcode.OK
}

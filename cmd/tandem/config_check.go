package main

import (
	"os"
	"path/filepath"

	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/paths"
	"github.com/emsnoel/tandem/internal/vcs"
)

// nearestManifest is the manifest `tandem paths manifest --from DIR` names:
// the nearest one at or above from, bounded by from's git toplevel, or the one
// in from itself outside a repository.
func nearestManifest(from string) (string, error) {
	abs, err := filepath.Abs(from)
	if err != nil {
		return "", err
	}
	boundary, err := vcs.Toplevel(abs)
	if err != nil {
		return paths.Manifest(abs), nil
	}
	return paths.NearestManifest(abs, boundary), nil
}

// cmdConfigCheckCommand answers "what is the check command, and which paths
// does it cover" for bin/tandem-unpair and bin/tandem-project. With --fields
// it prints the command, then each path, every one NUL-terminated.
func cmdConfigCheckCommand(args []string) int {
	p, asFields, rc := resolveProjectFlags("check-command", args)
	if rc != exitcode.OK {
		return rc
	}
	if asFields {
		writeFields(os.Stdout, append([]string{p.CheckCommand}, p.CheckPaths...)...)
		return exitcode.OK
	}
	paths := p.CheckPaths
	if paths == nil {
		paths = []string{}
	}
	return emitJSON(map[string]any{"check_command": p.CheckCommand, "check_paths": paths})
}

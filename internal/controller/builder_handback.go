package controller

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

// builderHandbackPath is the same before and after a restart. The nonce in
// the filename also lets usage collection find the prompt in session logs.
func builderHandbackPath(cfg runConfig, runID, nonce string) string {
	if cfg.BuilderHandbackRelay == "" {
		return ""
	}
	return filepath.Join(RunDir(filepath.Join(cfg.Root, cfg.BuilderHandbackRelay), runID), "builder", nonce)
}

func (c *Controller) prepareBuilderHandback(ctx context.Context, run state.Run, cfg runConfig, eff workflow.Effect) error {
	att, err := c.d.State.GetAttempt(ctx, eff.AttemptID)
	if err != nil {
		return err
	}
	task, err := c.taskOf(ctx, run, att)
	if err != nil {
		return err
	}
	p := attemptIdentity(cfg.Root, run.ID, att.ID, task.Ordinal)
	if p.Effect != eff.ID || p.Nonce != eff.Nonce {
		return fmt.Errorf("effect %s does not match the builder prompt for attempt %s", eff.ID, att.ID)
	}
	path := builderHandbackPath(cfg, p.Run, p.Nonce)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	// Keep the caller's cwd so --report can name a relative file. Only
	// forward the modes in the prompt, so callers cannot override identity.
	script := `#!/usr/bin/env bash
set -euo pipefail
case "$# ${1:-} ${3:-}" in
  '1 --started '|'2 --report '|'3 --report --stuck') ;;
  *) echo 'usage: handback --started | --report FILE [--stuck]' >&2; exit 2;;
esac
exec tandem-handback ` + p.handbackID() + " \"$@\"\n"
	return atomicWriteMode(dir, filepath.Base(path), []byte(script), 0700)
}

package controller

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

// prepareReviewHandback creates a command bound to one effect. Retrying prompt
// creation never truncates the verdict the reviewer may already have written.
func prepareReviewHandback(cfg runConfig, p *ReviewPacket) error {
	p.HandbackDir = filepath.Join(RunDir(cfg.relay(), p.RunID), "review", p.EffectID)
	if err := os.MkdirAll(p.HandbackDir, 0700); err != nil {
		return err
	}
	verdict := filepath.Join(p.HandbackDir, "verdict.json")
	f, err := os.OpenFile(verdict, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err == nil {
		err = f.Close()
	} else if os.IsExist(err) {
		err = nil
	}
	if err != nil {
		return err
	}
	role := cfg.ReviewerSession
	if p.Stage == stageIndependentVerification {
		role = cfg.PlannerSession
	}
	args := []string{"--task", fmt.Sprint(p.TaskOrdinal), "--role", role, "--run", p.RunID, "--attempt", p.AttemptID, "--job", p.JobID, "--effect", p.EffectID, "--nonce", p.Nonce}
	for i := range args {
		args[i] = shellQuote(args[i])
	}
	script := "#!/usr/bin/env bash\nset -euo pipefail\ncd -- " + shellQuote(cfg.Root) + "\n"
	script += "case \"${1:-}\" in\n  '') set -- --review-result " + shellQuote(verdict) + ";;\n  --started) set -- --review-started;;\n  *) echo 'usage: handback [--started]' >&2; exit 2;;\nesac\n"
	script += "exec tandem-handback " + strings.Join(args, " ") + " \"$@\"\n"
	path := filepath.Join(p.HandbackDir, "handback")
	if err := atomicWrite(p.HandbackDir, "handback", []byte(script)); err != nil {
		return err
	}
	return os.Chmod(path, 0700)
}

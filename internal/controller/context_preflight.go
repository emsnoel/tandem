package controller

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/emsnoel/tandem/internal/paths"
	"github.com/emsnoel/tandem/internal/plan"
	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/taskcontext"
	"github.com/emsnoel/tandem/internal/workflow"
)

// CheckPlan validates a plan when the settings for a future run are unknown.
func CheckPlan(path string) error {
	_, err := readPlanForPreflight(path)
	return err
}

func readPlanForPreflight(path string) (plan.Imported, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return plan.Imported{}, err
	}
	imp := plan.Import(string(raw))
	if err := imp.Err(); err != nil {
		return plan.Imported{}, fmt.Errorf("%s: %w", path, err)
	}
	return imp, nil
}

// CheckTaskContext measures every task's first prompt against the context
// limit before a run exists, so a task whose mandatory content does not fit is
// refused up front instead of pausing a new run.
//
// It builds the same packet the run builds, with zero ids and hashes of the
// real lengths. Memory and repair notes can only add to later prompts, and are
// checked when the attempt is created. It writes nothing.
func CheckTaskContext(req StartRequest) error {
	root, _, err := state.GitIdentity(req.Root)
	if err != nil {
		return err
	}
	imp, err := readPlanForPreflight(req.PlanPath)
	if err != nil {
		return err
	}
	cfg := runConfig{Root: root, PlanPath: req.PlanPath, CheckCommand: req.CheckCommand, ContextLimit: req.ContextLimit,
		Settings: settingsFor(req), BuilderHandbackRelay: filepath.Base(paths.Relay(root))}
	zeroID, zeroHash := strings.Repeat("0", 32), strings.Repeat("0", 64)
	var over []string
	need, limit := 0, 0
	for i, spec := range imp.Specs {
		_, err := taskcontext.Build(packetRequest(cfg, zeroID, zeroHash, zeroHash, zeroID, i+1, len(imp.Specs), spec, nil, ""))
		var tl *taskcontext.TooLargeError
		switch {
		case errors.As(err, &tl):
			over = append(over, fmt.Sprintf("%s needs %d", spec.ID, tl.Required))
			need, limit = max(need, tl.Required), tl.Limit
		case err != nil:
			return fmt.Errorf("task %s: %w", spec.ID, err)
		}
	}
	if len(over) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %d of %d tasks do not fit the context limit of %d bytes (%s); no run was started. "+
		"Raise the limit to at least %d with --context-limit, or context_limit in .tandem.json for tandem-handoff, or split the task",
		workflow.ErrContextTooLarge, len(over), len(imp.Specs), limit, strings.Join(over, ", "), need)
}

package controller

// A builder that acknowledged a task and then went quiet.
//
// Acknowledging clears the prompt's lease and nothing else waits on a started
// attempt, so a builder whose turn ended early would leave the run looking alive
// forever. Each tick therefore watches the current attempt's pane and pauses the
// run once the pane has stayed settled, in one unchanged turn, for SilenceWindow.
// A pane that stays blocked at an approval or question dialog for the same
// window pauses it too, as builder_blocked. Tandem never answers the dialog.
//
// Ticks are separate processes and the store's schema stays as it is, so the
// clock is a small file in the run's directory. It only ever delays a pause: a
// missing or unreadable file restarts it, and so does anything that says the
// pane or the attempt moved, or that nobody was watching.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/emsnoel/tandem/internal/runner"
	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

const (
	pauseBuilderSilent  = "builder_silent"
	pauseBuilderBlocked = "builder_blocked"
	silenceClockFile    = "builder-silence.json"
)

// SilenceWindow is how long a started builder's pane may stay settled, or
// blocked at a dialog, with nothing handed back. A variable so the shell tests can shorten it
// (TANDEM_SILENCE_WINDOW_MS, cmd/tandem/confirm_start.go).
var SilenceWindow = 10 * time.Minute

// silenceClock is the turn being timed and when it was first and last seen.
// Pause is the code the turn would pause with, which tells an idle pane from a
// blocked one.
type silenceClock struct {
	AttemptID string `json:"attempt_id"`
	Version   int64  `json:"version"`
	Seq       int    `json:"seq"`
	Session   string `json:"session"`
	Pause     string `json:"pause"`
	// Queued names the run's queued messages, so a new one restarts the clock
	// once and one that stays queued does not hold it off.
	Queued  []string `json:"queued"`
	SinceMS int64    `json:"since_ms"`
	SeenMS  int64    `json:"seen_ms"`
}

func (k silenceClock) sameTurn(o silenceClock) bool {
	return k.AttemptID == o.AttemptID && k.Version == o.Version && k.Seq == o.Seq && k.Session == o.Session &&
		k.Pause == o.Pause && strings.Join(k.Queued, "\n") == strings.Join(o.Queued, "\n")
}

// queuedNames is the run's queue by message, sorted. A claimed file and its
// unclaimed name are the same message: the bridge and a tick each rename it.
func queuedNames(run state.Run, cfg runConfig) []string {
	seen := map[string]bool{}
	names := []string{}
	for _, f := range queuedFiles(run, cfg) {
		n := strings.TrimSuffix(filepath.Base(f), ".taken")
		if !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names
}

func silenceClockPath(run state.Run, cfg runConfig) string {
	return filepath.Join(RunDir(cfg.relay(), run.ID), silenceClockFile)
}

// readSilenceClock reports false for a file that is missing or that is not a
// clock, which is the same as no clock at all.
func readSilenceClock(path string) (silenceClock, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return silenceClock{}, false
	}
	var k silenceClock
	if json.Unmarshal(raw, &k) != nil || k.AttemptID == "" || k.SinceMS <= 0 || k.SeenMS < k.SinceMS {
		return silenceClock{}, false
	}
	return k, true
}

// restartSilenceClock drops the clock. A file that cannot be removed is reported
// by the next write to it.
func restartSilenceClock(run state.Run, cfg runConfig) {
	_ = os.Remove(silenceClockPath(run, cfg))
}

// builderAttempt is the current attempt of the task being worked on, the one
// drive would step: the first unaccepted task that has attempts.
func (c *Controller) builderAttempt(ctx context.Context, run state.Run) (workflow.Attempt, bool, error) {
	tasks, err := c.d.State.RunTasks(ctx, run.ID)
	if err != nil {
		return workflow.Attempt{}, false, err
	}
	for _, t := range tasks {
		if t.AcceptedAttemptID != "" {
			continue
		}
		attempts, err := c.d.State.TaskAttempts(ctx, run.ID, t.ID)
		if err != nil {
			return workflow.Attempt{}, false, err
		}
		for _, a := range attempts {
			if a.Current {
				return a, true, nil
			}
		}
		if len(attempts) > 0 {
			return workflow.Attempt{}, false, nil
		}
	}
	return workflow.Attempt{}, false, nil
}

// observeBuilder observes the pane of the attempt's task prompt. An error is an
// answer that says nothing about the pane, which a gone pane's own pause handles.
func (c *Controller) observeBuilder(ctx context.Context, run state.Run, att workflow.Attempt) (runner.Observation, error) {
	effects, err := c.d.State.RunEffects(ctx, run.ID)
	if err != nil {
		return runner.Observation{}, err
	}
	// An attempt has one task prompt: its attempt_created event makes it, and a
	// retried send keeps the same effect. So the first match is the only one.
	for _, e := range effects {
		if e.Kind == workflow.EffectBuilderDispatch && e.AttemptID == att.ID {
			return c.d.Transport.Observe(ctx, e.ID)
		}
	}
	return runner.Observation{}, errors.New("the attempt has no task prompt")
}

// quietPause is the code a started builder's pane would pause with: settled is
// builder_silent, blocked at a dialog is builder_blocked, and anything else is
// not quiet.
func quietPause(obs runner.Observation) string {
	switch {
	case obs.State == runner.ObsCompleted:
		return pauseBuilderSilent
	case obs.State == runner.ObsRunning && obs.Blocked:
		return pauseBuilderBlocked
	}
	return ""
}

// quietCode reports whether code is one of the two pauses this file sets.
func quietCode(code string) bool {
	return code == pauseBuilderSilent || code == pauseBuilderBlocked
}

// watchSilence is the end of a running run's pass. It keeps the clock while the
// current builder has started, not submitted and sits settled or blocked with
// its queue unchanged, and pauses the run once that has lasted SilenceWindow. A
// handback the tree keeps from applying stays queued, so it counts as silence
// after the clock restarts once for it: the note says where the bridge logs why.
// Nothing is typed and nothing is ended, so a late handback still applies.
func (c *Controller) watchSilence(ctx context.Context, runID string, cfg runConfig) error {
	run, err := c.d.State.GetRun(ctx, runID)
	if err != nil || run.State != "running" {
		return err
	}
	att, found, err := c.builderAttempt(ctx, run)
	if err != nil {
		return err
	}
	if !found || att.Implementation != workflow.ImplementationRunning {
		restartSilenceClock(run, cfg)
		return nil
	}
	obs, err := c.observeBuilder(ctx, run, att)
	code := quietPause(obs)
	if err != nil || code == "" {
		restartSilenceClock(run, cfg)
		return nil
	}
	now := c.d.Now().UnixMilli()
	seen := silenceClock{AttemptID: att.ID, Version: att.Version, Seq: obs.StateSeq, Session: obs.Session,
		Pause: code, Queued: queuedNames(run, cfg), SinceMS: now, SeenMS: now}
	path := silenceClockPath(run, cfg)
	// A gap as long as the window is not watching, as when the machine slept.
	if last, ok := readSilenceClock(path); ok && last.sameTurn(seen) && now-last.SeenMS <= SilenceWindow.Milliseconds() {
		seen.SinceMS = last.SinceMS
	}
	body, err := json.Marshal(seen)
	if err != nil {
		return err
	}
	if err := atomicWrite(filepath.Dir(path), silenceClockFile, append(body, '\n')); err != nil {
		return err
	}
	if now-seen.SinceMS < SilenceWindow.Milliseconds() {
		return nil
	}
	return c.d.State.SetRunState(ctx, run.ID, "paused", code)
}

// continueAfterHandback clears a silent or blocked pause once the builder's handback has
// been applied, whichever process applied it. Only that clears it on a tick: a
// pane that merely moved waits for a recover, which a person asks for.
func (c *Controller) continueAfterHandback(ctx context.Context, runID string) error {
	run, err := c.d.State.GetRun(ctx, runID)
	if err != nil || run.State != "paused" || !quietCode(run.PauseCode) {
		return err
	}
	att, found, err := c.builderAttempt(ctx, run)
	if err != nil || (found && att.Implementation == workflow.ImplementationRunning) {
		return err
	}
	return c.d.State.SetRunState(ctx, run.ID, "running", "")
}

// recoverSilence is a recover's part. Whoever recovers, nobody can say the pane
// was watched before it, so the clock starts again: that is how a restart of the
// bridge restarts it. The exceptions are the two quiet pauses. builder_silent
// needs the clock to tell whether the turn moved on, and clears if it did.
// builder_blocked clears once the pane is seen past its dialog.
func (c *Controller) recoverSilence(ctx context.Context, runID string) error {
	run, cfg, err := c.load(ctx, runID)
	if err != nil {
		return err
	}
	if run.State != "paused" || !quietCode(run.PauseCode) {
		restartSilenceClock(run, cfg)
		return nil
	}
	moved, err := c.turnMovedOn(ctx, run, cfg)
	if err != nil || !moved {
		return err
	}
	restartSilenceClock(run, cfg)
	return c.d.State.SetRunState(ctx, run.ID, "running", "")
}

// turnMovedOn compares the quiet attempt's pane with the turn the clock last
// timed. A blocked pause has moved once the pane is working or settled and no
// longer blocked. For a silent one, with no clock to compare, only a pane seen
// working has moved. A pane the host cannot answer for has not.
func (c *Controller) turnMovedOn(ctx context.Context, run state.Run, cfg runConfig) (bool, error) {
	att, found, err := c.builderAttempt(ctx, run)
	if err != nil || !found || att.Implementation != workflow.ImplementationRunning {
		return false, err // a handback applied is continueAfterHandback's to clear
	}
	obs, err := c.observeBuilder(ctx, run, att)
	if err != nil || obs.Blocked {
		return false, nil
	}
	if obs.State == runner.ObsRunning || (run.PauseCode == pauseBuilderBlocked && obs.State == runner.ObsCompleted) {
		return true, nil
	}
	last, ok := readSilenceClock(silenceClockPath(run, cfg))
	now := silenceClock{AttemptID: att.ID, Version: att.Version, Seq: obs.StateSeq, Session: obs.Session, Pause: pauseBuilderSilent, Queued: last.Queued}
	return ok && obs.State == runner.ObsCompleted && !last.sameTurn(now), nil
}

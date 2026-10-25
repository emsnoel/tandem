package main

import (
	"path/filepath"

	"github.com/emsnoel/tandem/internal/controller"
	"github.com/emsnoel/tandem/internal/mailwatch"
	"github.com/emsnoel/tandem/internal/state"
)

// mailSource is what runAgentWatch reads the mailbox watch through
// (*mailwatch.Watch live, a fake in tests).
type mailSource interface {
	Wakes() <-chan mailwatch.Wake
	Errors() <-chan error
	Close() error
}

// openMailbox watches the relay's queue and run marker, and for a durable run
// the run's queue and the nudge in stateRoot. It returns a line to print at
// once when the run is not the expected one or a message is already queued.
// Watching before looking leaves no gap after the caller's scan. An unreadable
// marker counts as no run, so the bridge does not spin on it.
func openMailbox(relay, run, stateRoot string) (*mailwatch.Watch, string, error) {
	mw, err := mailwatch.New()
	if err != nil {
		return nil, "", err
	}
	marker := filepath.Join(relay, controller.ActiveRunFile)
	arm := func() error {
		if err := mw.AddQueue(filepath.Join(relay, "q")); err != nil {
			return err
		}
		if err := mw.AddFile(marker, "run"); err != nil {
			return err
		}
		if run == "" {
			return nil
		}
		if err := mw.AddQueue(filepath.Join(controller.RunDir(relay, run), "q")); err != nil {
			return err
		}
		return mw.AddFile(filepath.Join(stateRoot, state.NudgeFile), "nudge")
	}
	if err := arm(); err != nil {
		mw.Close()
		return nil, "", err
	}
	current := ""
	if ar, err := controller.ReadActiveRun(relay); err == nil && ar != nil {
		current = ar.RunID
	}
	if current != run {
		return mw, "run\t" + marker, nil
	}
	if p, ok := mw.Waiting(); ok {
		return mw, "queued\t" + p, nil
	}
	return mw, "", nil
}

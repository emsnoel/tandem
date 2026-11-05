package controller

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/emsnoel/tandem/internal/runner"
	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

// Reading what a builder sent and what its pane shows, to learn whether a prompt
// was read.

// hasUnknownWriter reports whether a builder prompt's delivery is unconfirmed,
// after reading what the builder queued, which is what proves one was read, and
// trying to confirm the rest by observation.
func (c *Controller) hasUnknownWriter(ctx context.Context, run state.Run) (bool, error) {
	cfg, err := c.config(ctx, run)
	if err != nil {
		return false, err
	}
	// A message that fails here is tried, and reported, by the pass that follows.
	_ = c.processMailbox(ctx, run, cfg)
	clear, err := c.reconcileWriter(ctx, run)
	return !clear, err
}

// reconcileWriter observes every unconfirmed builder prompt of the run. A
// prompt whose nonce is seen on a live agent is acknowledged. It returns true
// when no unconfirmed prompt is left.
func (c *Controller) reconcileWriter(ctx context.Context, run state.Run) (bool, error) {
	effects, err := c.d.State.RunEffects(ctx, run.ID)
	if err != nil {
		return false, err
	}
	sort.Slice(effects, func(i, j int) bool { return effects[i].ID < effects[j].ID })
	clear := true
	for _, e := range effects {
		if e.Kind != workflow.EffectBuilderDispatch || (e.State != "delivering" && e.State != "delivery_unknown") {
			continue
		}
		obs, err := c.d.Transport.Observe(ctx, e.ID)
		if err != nil {
			return false, err
		}
		if obs.Nonce == e.Nonce && (obs.State == runner.ObsRunning || obs.State == runner.ObsCompleted) {
			if err := c.d.State.AcknowledgeEffect(ctx, e.ID, e.Nonce); err != nil {
				return false, err
			}
			continue
		}
		clear = false
	}
	return clear, nil
}

// processMailbox ingests the run's queued builder messages, at most
// maxMailboxPerTick, replaying any claimed before a crash first.
func (c *Controller) processMailbox(ctx context.Context, run state.Run, cfg runConfig) error {
	files := queuedFiles(run, cfg)
	if len(files) > maxMailboxPerTick {
		files = files[:maxMailboxPerTick]
	}
	deps := c.mailboxDeps(run, cfg)
	var first error
	for _, f := range files {
		claimed := f
		if !strings.HasSuffix(f, ".taken") {
			claimed = f + ".taken"
			if err := os.Rename(f, claimed); err != nil {
				continue
			}
		}
		if _, err := ProcessClaimed(ctx, deps, claimed); err != nil {
			// Undecided: put it back so the next tick tries again.
			if _, serr := os.Stat(claimed); serr == nil {
				_ = os.Rename(claimed, strings.TrimSuffix(claimed, ".taken"))
			}
			// A builder still in its turn is waited for, not a failed tick.
			if first == nil && !errors.Is(err, ErrBuilderWorking) {
				first = err
			}
		}
	}
	return first
}

// queuedFiles lists the run's mailbox, claimed messages first.
func queuedFiles(run state.Run, cfg runConfig) []string {
	dir := RunDir(cfg.relay(), run.ID)
	var files []string
	for _, pattern := range []string{"*.json.taken", "*.json"} {
		m, _ := filepath.Glob(filepath.Join(dir, "q", pattern))
		sort.Strings(m)
		files = append(files, m...)
	}
	return files
}

// queuedAcknowledgements are the open deliveries a queued message proves were
// read: it carries the effect's nonce. A message that cannot be parsed, or names
// another nonce, proves nothing. Keyed by effect id.
func (c *Controller) queuedAcknowledgements(ctx context.Context, run state.Run, cfg runConfig) map[string]string {
	out := map[string]string{}
	for _, f := range queuedFiles(run, cfg) {
		raw, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		env, err := ParseEnvelope(raw)
		if err != nil || env.RunID != run.ID {
			continue
		}
		eff, err := c.d.State.GetEffect(ctx, env.EffectID)
		if err != nil || eff.RunID != run.ID || eff.Nonce != env.Nonce || (eff.State != "delivering" && eff.State != "delivery_unknown") {
			continue
		}
		out[eff.ID] = env.Nonce
	}
	return out
}

// readLateAcknowledgements closes the deliveries a stopped run's mailbox proves
// were read. It only acknowledges: a stopped run takes no result and does not
// resume, so the messages themselves are not applied.
func (c *Controller) readLateAcknowledgements(ctx context.Context, run state.Run) error {
	cfg, err := c.config(ctx, run)
	if err != nil {
		return err
	}
	for id, nonce := range c.queuedAcknowledgements(ctx, run, cfg) {
		if err := c.d.State.AcknowledgeEffect(ctx, id, nonce); err != nil {
			return err
		}
	}
	return nil
}

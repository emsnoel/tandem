package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/emsnoel/tandem/internal/rollout"
	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

// usageSource names a source of usage that is not an event: a marker.
func usageSource(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// collectUsage reads and stores usage for every builder job that was sent. It
// reports notes about gaps and which tasks have had a job sent.
func (c *Controller) collectUsage(ctx context.Context, run state.Run, cfg runConfig) (notes []string, sent map[string]bool, err error) {
	sent = map[string]bool{}
	created, err := c.d.State.RunCreatedMS(ctx, run.ID)
	if err != nil {
		return nil, nil, err
	}
	from, to := time.UnixMilli(created), c.d.Now()
	effects, err := c.d.State.RunEffects(ctx, run.ID)
	if err != nil {
		return nil, nil, err
	}
	dispatch := map[string]workflow.Effect{}
	for _, e := range effects {
		if e.Kind == workflow.EffectBuilderDispatch {
			switch e.State {
			case "delivering", "acknowledged", "delivery_unknown":
				dispatch[e.AttemptID] = e
			}
		}
	}
	tasks, err := c.d.State.RunTasks(ctx, run.ID)
	if err != nil {
		return nil, nil, err
	}
	for _, t := range tasks {
		attempts, err := c.d.State.TaskAttempts(ctx, run.ID, t.ID)
		if err != nil {
			return nil, nil, err
		}
		for _, att := range attempts {
			eff, ok := dispatch[att.ID]
			if !ok {
				continue
			}
			sent[t.ID] = true
			jobs, err := c.d.State.AttemptJobs(ctx, att.ID, "builder")
			if err != nil {
				return nil, nil, err
			}
			for _, job := range jobs {
				n, err := c.collectJob(ctx, cfg, att, job, eff, from, to)
				if err != nil {
					return nil, nil, err
				}
				notes = append(notes, n...)
			}
		}
	}
	return notes, sent, nil
}

// sessionOfEffect reads the session a transport reported when it accepted the
// effect's prompt.
func (c *Controller) sessionOfEffect(ctx context.Context, eff workflow.Effect) string {
	h, found, err := c.d.State.EffectReceiptHash(ctx, eff.ID)
	if err != nil || !found {
		return ""
	}
	raw, err := c.d.Artifacts.ReadAll(ctx, h, 1<<20)
	if err != nil {
		return ""
	}
	var r struct {
		ExternalID string `json:"external_id"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return ""
	}
	return r.ExternalID
}

// recordBuilderSession stores the session the builder ran a submitted task in.
// A fresh Codex pane has no session at send time, so the pane is asked now,
// and failing that the conversation is found by the task's nonce in its log.
// Not finding one leaves the job's usage as a gap.
func (c *Controller) recordBuilderSession(ctx context.Context, cfg runConfig, env Envelope) error {
	eff, err := c.d.State.GetEffect(ctx, env.EffectID)
	if err != nil {
		return err
	}
	session := ""
	if obs, err := c.observeEffect(ctx, cfg, eff); err == nil {
		session = obs.Session
	}
	if f, ok := c.d.UsageSource.(SessionFinder); ok && session == "" {
		created, err := c.d.State.RunCreatedMS(ctx, env.RunID)
		if err != nil {
			return err
		}
		session, _ = f.SessionWith(ctx, cfg.BuilderKind, eff.Nonce, time.UnixMilli(created), c.d.Now())
	}
	if session == "" {
		return nil
	}
	return c.d.State.SetBuilderJobSession(ctx, env.JobID, session)
}

func (c *Controller) collectJob(ctx context.Context, cfg runConfig, att workflow.Attempt, job workflow.Job, eff workflow.Effect, from, to time.Time) ([]string, error) {
	var notes []string
	put := func(source, model string, u Usage, clears ...string) error {
		err := c.d.State.PutUsage(ctx, state.UsageRecord{ID: derive("usage", source), AttemptID: att.ID, JobID: job.ID, SourceID: source, Model: model, Usage: u}, clears...)
		if errors.Is(err, state.ErrUsageSourceTaken) {
			// Another job's session names this log. Counting it again would
			// double the figures and refusing the report would hide all the rest.
			notes = append(notes, fmt.Sprintf("attempt %d: a log line is already counted for another job (%v); not counted again", att.Ordinal, err))
			return nil
		}
		return err
	}
	gap := func(kind, session, why string) error {
		notes = append(notes, fmt.Sprintf("attempt %d, session %s: %s", att.Ordinal, orNone(session), why))
		u := Usage{Completeness: workflow.UsageUnavailable, InputSemantics: workflow.SemanticsUnknown}
		return put(usageSource(kind, job.ID, session), "unknown", u)
	}
	session := c.sessionOfEffect(ctx, eff)
	if session == "" {
		session = job.SessionID
	}
	if session == "" {
		return notes, gap("unavailable", "", "the transport reported no session for this job, so its log cannot be found")
	}
	var streams []rollout.Stream
	if c.d.UsageSource == nil {
		return notes, gap("unavailable", session, "no usage source is configured")
	}
	streams, err := c.d.UsageSource.Collect(ctx, cfg.BuilderKind, session, from, to)
	switch {
	case errors.Is(err, workflow.ErrUnsupported):
		return notes, gap("unavailable", session, err.Error())
	case err != nil:
		return notes, gap("unavailable", session, "the logs could not be searched: "+err.Error())
	}
	if len(streams) == 0 {
		return notes, gap("unavailable", session, "no log was found for this session")
	}
	covered := rollout.Covered(streams)
	for _, s := range streams {
		id := s.SessionID
		if id == "" {
			id = s.File
		}
		if by, skip := covered[s.SessionID]; skip && s.SessionID != "" {
			notes = append(notes, fmt.Sprintf("session %s: included in the totals of its parent %s, not counted again", id, by))
			continue
		}
		unavail, partial := usageSource("unavailable", job.ID, id), usageSource("partial", job.ID, id)
		if s.Unavailable != "" {
			notes = append(notes, fmt.Sprintf("session %s: %s", id, s.Unavailable))
			if err := put(unavail, "unknown", Usage{Completeness: workflow.UsageUnavailable, InputSemantics: workflow.SemanticsUnknown}); err != nil {
				return notes, err
			}
			continue
		}
		for _, d := range s.Diagnostics {
			notes = append(notes, fmt.Sprintf("session %s: %s", id, d))
		}
		// What stops being true once this log has been read: a marker that it was
		// unreadable, one that the job had no session yet (its session arrived
		// with the submission), and one that it was cut off if it no longer is.
		clears := []string{unavail, usageSource("unavailable", job.ID, "")}
		if !s.Truncated && s.Rejected == 0 {
			clears = append(clears, partial)
		}
		for i, d := range s.Deltas {
			u, err := workflow.NormalizeUsage(workflow.Int64(d.Counters.Input), workflow.Int64(d.Counters.Cached), workflow.Int64(d.Counters.Output), workflow.SemanticsIncludesCache)
			if err != nil {
				notes = append(notes, fmt.Sprintf("session %s line %d: %v", id, d.Line, err))
				continue
			}
			model := d.Model
			if model == "" {
				model = "unknown"
			}
			var cl []string
			if i == 0 {
				cl = clears
			}
			if err := put(d.SourceID, model, u, cl...); err != nil {
				return notes, err
			}
		}
		if s.Truncated || s.Rejected > 0 {
			if err := put(partial, "unknown", Usage{Completeness: workflow.UsagePartial, InputSemantics: workflow.SemanticsIncludesCache}, unavail); err != nil {
				return notes, err
			}
		}
	}
	return notes, nil
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

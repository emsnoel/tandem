package controller

// A builder that went idle without handing back gets one reminder.
//
// A builder can finish its work and end its turn while its handback never ran,
// for example when the call was moved to the background and waited on input
// for good (#90). The silence clock in silence.go times that turn. Once the
// pane has stayed settled in it for RemindWindow with nothing queued, tandem
// types one reminder that repeats the handback command. If the builder then
// stays settled for another RemindWindow without a handback, the run pauses as
// builder_silent and the planner gets the note.
//
// The reminder is typed at most once per attempt, whatever became of it. A
// file in the run's directory names the attempt it was typed for, and it is
// written before the reminder is typed, so a crash cannot repeat it.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/emsnoel/tandem/internal/runner"
	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

const (
	// effectBuilderReminder is the transport kind of a reminder: typed only
	// into a settled pane, in the same conversation, and watched to start.
	effectBuilderReminder = "builder_reminder"
	reminderFile          = "builder-reminded"
)

// RemindWindow is how long a started builder's pane may stay settled in one
// turn, with nothing handed back or queued, before tandem reminds it. After a
// reminder it is also how long the pane may stay settled before the run
// pauses. A window as long as SilenceWindow turns the reminder off. A variable
// so the shell tests can shorten it (TANDEM_REMIND_WINDOW_MS,
// cmd/tandem/confirm_start.go).
var RemindWindow = 2 * time.Minute

func reminderPath(relay, runID string) string {
	return filepath.Join(RunDir(relay, runID), reminderFile)
}

// Reminded reports whether tandem has typed, or tried to type, the reminder
// for an attempt of the run whose relay folder is relay.
func Reminded(relay, runID, attemptID string) bool {
	raw, err := os.ReadFile(reminderPath(relay, runID))
	return err == nil && attemptID != "" && strings.TrimSpace(string(raw)) == attemptID
}

// quietDue says what a settled or blocked turn timed by the clock calls for
// now: a reminder, a pause, or nothing yet. Only an idle pane with an empty
// queue is reminded: a dialog is never typed into, and a queued handback is a
// handback.
func quietDue(code string, clock silenceClock, reminded bool, now int64) (remind, pause bool) {
	quiet := now - clock.SinceMS
	if code == pauseBuilderSilent && reminded && quiet >= RemindWindow.Milliseconds() {
		return false, true
	}
	if quiet >= SilenceWindow.Milliseconds() {
		return false, true
	}
	remind = code == pauseBuilderSilent && !reminded && len(clock.Queued) == 0 && quiet >= RemindWindow.Milliseconds()
	return remind, false
}

// remindBuilder types the one reminder for the attempt. The claim is written
// first; a launch its collaboration holds keeps the claim and types nothing,
// so the planner hears of the silence instead.
func (c *Controller) remindBuilder(ctx context.Context, run state.Run, cfg runConfig, att workflow.Attempt) error {
	task, err := c.attemptTask(ctx, run, att)
	if err != nil {
		return err
	}
	dispatch := derive(att.ID, "created")
	launchID := derive(att.ID, "reminder")
	jobID := derive(att.ID, "builder-job")
	if cfg.Collaboration != "" {
		err := c.admitSource(ctx, run, cfg, launchID, att.ID, jobID, cfg.BuilderKind, sourceID(state.EffectID(dispatch, 0)))
		if err != nil && !held(err) {
			return err
		}
		if err != nil {
			return claimReminder(cfg, run, att)
		}
	}
	p := attemptIdentity(cfg.Root, run.ID, att.ID, task.Ordinal)
	hash, err := c.put(ctx, reminderText(p))
	if err != nil {
		return err
	}
	if err := claimReminder(cfg, run, att); err != nil {
		return err
	}
	_, err = c.d.Transport.Send(ctx, runner.Delivery{Kind: effectBuilderReminder, EffectID: launchID, AttemptID: att.ID,
		PromptHash: hash, AgentKind: cfg.BuilderKind, SessionID: cfg.BuilderSession})
	if err != nil {
		// Never typed again: the pause a window later tells the planner.
		return fmt.Errorf("the reminder to hand back task %d may not have reached the builder: %w", task.Ordinal, err)
	}
	return nil
}

func claimReminder(cfg runConfig, run state.Run, att workflow.Attempt) error {
	path := reminderPath(cfg.relay(), run.ID)
	return atomicWrite(filepath.Dir(path), reminderFile, []byte(att.ID+"\n"))
}

// attemptTask is the task row of an attempt.
func (c *Controller) attemptTask(ctx context.Context, run state.Run, att workflow.Attempt) (state.TaskRow, error) {
	tasks, err := c.d.State.RunTasks(ctx, run.ID)
	if err != nil {
		return state.TaskRow{}, err
	}
	for _, t := range tasks {
		if t.ID == att.TaskID {
			return t, nil
		}
	}
	return state.TaskRow{}, fmt.Errorf("%w: attempt %s names task %s, which the run does not have", workflow.ErrCorruptArtifact, att.ID, att.TaskID)
}

// reminderText asks the builder for the handback it has not sent, with the
// command the task prompt gave it.
func reminderText(p promptData) string {
	return fmt.Sprintf("Tandem has no handback from you for task %d, and your pane has been idle. "+
		"If you finished, or could not finish, hand back now. %s\n\n    tandem-handback %s --report <file>\n\n"+
		"Add --stuck if you could not finish. If you are still working, carry on and hand back when you are done.",
		p.Task, handbackRule, p.handbackID())
}

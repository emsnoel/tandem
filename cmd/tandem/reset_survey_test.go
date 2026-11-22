package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/herdr"
	"github.com/emsnoel/tandem/internal/runner"
)

// agyPane models what an agy 1.5.14 pane did in the run that reproduced #118.
// After a turn it may draw its feedback survey in place of the input box.
// Text typed while the survey shows answers it and is dropped, so a /new
// starts no conversation. Its Skip key ("0") dismisses it. A /new typed at
// the input starts a conversation, which agy's process log records.
type agyPane struct {
	t       *testing.T
	survey  bool // the survey is on screen
	created bool // a /new reached the input and started a conversation
	// surveyAfterNew draws the survey again over the fresh screen.
	surveyAfterNew bool
	// logged is whether the new conversation reaches agy's process log.
	logged bool
	events []string // "press 0", "submit /new", in order
}

func (p *agyPane) deps(f *fakeHerdr) resetDeps {
	d := f.registryDeps()
	d.screen = func(string) (string, error) {
		switch {
		case p.survey:
			return screenFixture(p.t, "agy-feedback-survey.txt"), nil
		case p.created:
			return screenFixture(p.t, "agy-main-after-new.txt"), nil
		}
		return screenFixture(p.t, "agy-main-after-turn.txt"), nil
	}
	d.press = func(_, key string) error {
		p.events = append(p.events, "press "+key)
		if p.survey && key == "0" {
			p.survey = false
		}
		return nil
	}
	d.submit = func(_, text string) error {
		p.events = append(p.events, "submit "+text)
		f.submitted = append(f.submitted, text)
		if p.survey {
			p.survey = false // the survey took the text
			return nil
		}
		p.created = true
		p.survey = p.surveyAfterNew
		return nil
	}
	d.fresh = func(string, *herdr.Agent, time.Time) (string, bool) {
		if p.created && p.logged {
			return "c-new", true
		}
		return "", true
	}
	return d
}

func idleAgy() *fakeHerdr {
	return &fakeHerdr{states: []*herdr.Agent{kindAgent("agy", "idle", "s1", 5)}}
}

// Issue #118: the survey was on screen when the build reset the builder, so
// /new was dropped and the reset ran out unconfirmed, pausing the run. The
// survey is now skipped before anything is typed.
func TestResetSkipsAgySurveyBeforeTypingNew(t *testing.T) {
	p := &agyPane{t: t, survey: true, logged: true}
	tok, session, why, err := runReset(p.deps(idleAgy()), "builder", rootWith(t, agyBuilder, ""), 15*time.Second, false)
	if err != nil || tok != "reset" || session != "c-new" || why != "" {
		t.Fatalf("got %q, %q, why %q, %v; want reset into c-new", tok, session, why, err)
	}
	if want := []string{"press 0", "submit /new"}; strings.Join(p.events, ",") != strings.Join(want, ",") {
		t.Errorf("events %q, want %q: skip the survey, then type /new once", p.events, want)
	}
}

// A survey drawn over the fresh screen hides the proof the screen gives. It is
// skipped, and the fresh screen then confirms a reset the log missed.
func TestResetSkipsAgySurveyDrawnAfterNew(t *testing.T) {
	p := &agyPane{t: t, surveyAfterNew: true}
	tok, _, _, err := runReset(p.deps(idleAgy()), "builder", rootWith(t, agyBuilder, ""), 15*time.Second, false)
	if err != nil || tok != "sent" {
		t.Fatalf("got %q, %v; want sent from the fresh screen", tok, err)
	}
	if want := []string{"submit /new", "press 0"}; strings.Join(p.events, ",") != strings.Join(want, ",") {
		t.Errorf("events %q, want %q", p.events, want)
	}
}

// A dry run decides without touching the pane, survey or not.
func TestResetDryRunLeavesAgySurvey(t *testing.T) {
	p := &agyPane{t: t, survey: true}
	tok, _, _, err := runReset(p.deps(idleAgy()), "builder", rootWith(t, agyBuilder, ""), time.Second, true)
	if err != nil || tok != "skip:off-main" || len(p.events) != 0 {
		t.Fatalf("got %q, %v, events %q; want skip:off-main and nothing sent", tok, err, p.events)
	}
}

// An unconfirmed reset says what the pane showed, so a pause names its cause.
func TestUnconfirmedResetSaysWhatThePaneShowed(t *testing.T) {
	f := &fakeHerdr{
		screen: screenFixture(t, "agy-main-after-turn.txt"),
		states: []*herdr.Agent{kindAgent("agy", "idle", "s1", 5)},
		fresh:  []string{""},
	}
	tok, _, why, err := runReset(f.registryDeps(), "builder", rootWith(t, agyBuilder, ""), 2*time.Second, false)
	if err != nil || tok != "unconfirmed" {
		t.Fatalf("got %q, %v; want unconfirmed", tok, err)
	}
	for _, want := range []string{"Herdr reports it idle", "agy's own log shows no new conversation", "its screen shows no fresh conversation", "its screen ends"} {
		if !strings.Contains(why, want) {
			t.Errorf("why %q lacks %q", why, want)
		}
	}
}

func TestScreenTailQuotesTheLastLines(t *testing.T) {
	got := screenTail("banner\n\n  one   two  \nthree\n\n? for shortcuts      auto\n\n")
	if want := "one two / three / ? for shortcuts auto"; got != want {
		t.Errorf("screenTail = %q, want %q", got, want)
	}
	long := strings.Repeat("x", 500)
	if got := []rune(screenTail(long)); len(got) != screenTailRunes || got[len(got)-1] != '…' {
		t.Errorf("long tail has %d runes, want %d ending in …", len(got), screenTailRunes)
	}
}

// A prompt for an agy pane showing its feedback survey skips the survey and is
// typed once, into the input the survey hid (#118).
func TestPromptAsSkipsAgySurveyFirst(t *testing.T) {
	p := newPane("idle")
	p.kind = "agy"
	p.screens = []string{screenFixture(t, "agy-feedback-survey.txt")}
	p.afterKey = []string{screenFixture(t, "agy-main-after-turn.txt")}
	if code := promptAs(p.deps(), "planner", "verdict", deliverPolicy{confirm: true}, io.Discard); code != exitcode.OK {
		t.Fatalf("exit %d", code)
	}
	if len(p.keys) != 1 || p.keys[0] != "0" || len(p.prompts) != 1 {
		t.Fatalf("keys %q prompts %q; want the survey skipped with 0, then one prompt", p.keys, p.prompts)
	}
}

// A survey that stays after its Skip key gets nothing typed into it.
func TestPromptAsTypesNothingIntoAnAgySurveyThatStays(t *testing.T) {
	p := newPane("idle")
	p.kind = "agy"
	p.screens = []string{screenFixture(t, "agy-feedback-survey.txt")}
	var errb bytes.Buffer
	code := promptAs(p.deps(), "planner", "verdict", deliverPolicy{confirm: true}, &errb)
	if code != exitcode.HerdrError || !strings.Contains(errb.String(), "feedback survey") || len(p.prompts) != 0 {
		t.Fatalf("exit %d stderr %q prompts %q; want a refusal naming the survey and nothing typed", code, errb.String(), p.prompts)
	}
}

// A build's pause names what the pane showed when its reset went unconfirmed.
func TestHerdrSendNamesWhyAResetWasUnconfirmed(t *testing.T) {
	p := newPane("idle")
	p.resetTok, p.resetWhy = "unconfirmed", "Herdr reports it idle; its screen ends \"[0] Skip\""
	_, err := transportFor(p).Send(context.Background(), delivery("builder_dispatch", "builder"))
	if !runner.IsNotSent(err) || !strings.Contains(err.Error(), "(unconfirmed: Herdr reports it idle; its screen ends") {
		t.Fatalf("err %v; want the reason in the refusal", err)
	}
}

package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/runner"
)

// A closing read that fails is Herdr not answering, not a prompt that never
// started: the full check reports it as the error it is (exit 3 for an
// unreachable socket, which routes a sandboxed caller to the mailbox).
func TestPromptAsFullModeReportsAnUnreachableClosingRead(t *testing.T) {
	p := newPane("idle")
	var errb bytes.Buffer
	code := promptAs(unreachableAfterWatch(p), "builder", "hi", deliverPolicy{confirm: true}, &errb)
	if code != exitcode.Unreachable || strings.HasPrefix(errb.String(), "agent_never_started") || !strings.Contains(errb.String(), "no socket") {
		t.Fatalf("exit %d stderr %q", code, errb.String())
	}
	if len(p.prompts) != 1 {
		t.Errorf("typed %d times", len(p.prompts))
	}
}

// Send typed the prompt before the read failed, so whether it arrived is
// unknown: the error must not be NotSent, which would let it be typed again.
func TestHerdrSendReportsAFailedClosingReadAsUncertain(t *testing.T) {
	p := newPane("idle")
	tr := newHerdrTransport(transportSpec{Kind: "codex", Builder: "builder", Planner: "planner", Root: "/repo"}, unreachableAfterWatch(p))
	_, err := tr.Send(context.Background(), delivery("review_prompt", "builder"))
	if err == nil || runner.IsNotSent(err) || !strings.Contains(err.Error(), "no socket") {
		t.Fatalf("err = %v, want an uncertain error naming the failed read", err)
	}
	if len(p.prompts) != 1 {
		t.Errorf("typed %d times", len(p.prompts))
	}
}

// Before anything is typed there is no prompt that might still land, so the
// wait for a dialog to clear ends on its closing read as soon as the stream
// breaks, instead of holding the bridge's queue until the deadline.
func TestPromptAsBlockedWaitEndsAtOnceOnABrokenStream(t *testing.T) {
	p := newPane("blocked")
	d := p.deps()
	watch := d.watch
	d.watch = func(names ...string) (eventWatch, error) {
		w, err := watch(names...)
		return brokenStream{w}, err
	}
	start := p.now
	var errb bytes.Buffer
	code := promptAs(d, "builder", "hi", deliverPolicy{confirm: true, wait: 2 * time.Minute}, &errb)
	if code != exitcode.HerdrError || !strings.HasPrefix(errb.String(), "agent_blocked") {
		t.Fatalf("exit %d stderr %q", code, errb.String())
	}
	if waited := p.now.Sub(start); waited >= time.Minute {
		t.Errorf("waited %v on a broken stream before typing anything", waited)
	}
	if len(p.prompts) != 0 {
		t.Error("typed into a blocked pane")
	}
}

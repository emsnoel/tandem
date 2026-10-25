package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emsnoel/tandem/internal/controller"
	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/herdr"
	"github.com/emsnoel/tandem/internal/runner"
)

const modelError = "There's an issue with the selected model (sonnet-5.5). It may not exist or you may not have access to it."

// promptAndConfirm types a prompt into p the way Send does and confirms it.
func promptAndConfirm(t *testing.T, p *fakePane) startResult {
	t.Helper()
	d := p.deps()
	w, err := d.watch("builder")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	a := w.Snapshot("builder")
	mark := markTokens(d, a)
	if err := d.prompt("builder", "PROMPT"); err != nil {
		t.Fatal(err)
	}
	return confirmStart(d, w, "builder", a, mark)
}

// The point of watching events: a turn that starts and ends between two polls is seen,
// not inferred from a counter.
func TestConfirmStartSeesAFastTurnWithoutTheCounter(t *testing.T) {
	p := newPane("idle")
	p.endAfter, p.paneText = 1, modelError
	p.seqFrozen = true // Herdr's counter does not move: only the events show the turn
	if r := promptAndConfirm(t, p); r.outcome != endedAtOnce {
		t.Fatalf("outcome %q, want %q", r.outcome, endedAtOnce)
	}
}

func TestConfirmStartDecidesFromTheClosingReadWhenTheWatchBreaks(t *testing.T) {
	p := newPane("idle")
	d := p.deps()
	a, _ := d.get("builder")
	start := p.now
	w := &scriptWatch{snaps: map[string]*herdr.Agent{"builder": a}, clock: &p.now,
		steps: []scriptStep{{after: time.Second, err: &herdr.Error{Code: "unreachable", Unreachable: true}}}}
	_ = d.prompt("builder", "PROMPT") // the fake is now working
	r := confirmStart(d, w, "builder", a, tokenMark{})
	if r.outcome != started {
		t.Fatalf("outcome %q, want started from the closing read", r.outcome)
	}
	// A break ends no wait early: the closing read comes at the deadline.
	if waited := p.now.Sub(start); waited < landWithin {
		t.Errorf("decided after %v, before landWithin (%v)", waited, landWithin)
	}
}

// A stream that breaks early must not turn a prompt that is still to land into
// never_started: the wait runs to its deadline first.
func TestConfirmStartWaitsOutADeadlineWhenTheWatchBreaksEarly(t *testing.T) {
	p := newPane("idle")
	d := p.deps()
	a, _ := d.get("builder")
	start := p.now
	w := &scriptWatch{snaps: map[string]*herdr.Agent{"builder": a}, clock: &p.now,
		steps: []scriptStep{{after: 100 * time.Millisecond, err: &herdr.Error{Code: "unreachable", Unreachable: true}}}}
	d.sleep = func(dur time.Duration) {
		p.now = p.now.Add(dur)
		if p.now.Sub(start) >= 2*time.Second {
			p.status = "working" // the prompt lands 2 s in
		}
	}
	if r := confirmStart(d, w, "builder", a, tokenMark{}); r.outcome != started {
		t.Fatalf("outcome %q, want started: the prompt landed before the deadline", r.outcome)
	}
}

func TestConfirmStartReturnsAtOnceWhenTheTurnEnds(t *testing.T) {
	p := newPane("idle")
	p.endAfter, p.promptTokens = 2, 50
	start := p.now
	if r := promptAndConfirm(t, p); r.outcome != started {
		t.Fatalf("outcome %q, want started (tokens grew)", r.outcome)
	}
	if waited := p.now.Sub(start); waited >= failWithin {
		t.Errorf("waited %v; an ended turn returns when it ends", waited)
	}
}

func TestConfirmStartKeepsATurnThatIsStillWorking(t *testing.T) {
	p := newPane("idle")
	start := p.now
	r := promptAndConfirm(t, p)
	if r.outcome != started {
		t.Fatalf("outcome %q, want %q", r.outcome, started)
	}
	// A turn counts only once it has outlived failWithin.
	if waited := p.now.Sub(start); waited < failWithin {
		t.Errorf("returned after %v, before failWithin (%v)", waited, failWithin)
	}
}

func TestConfirmStartReportsAPromptThatNeverStarted(t *testing.T) {
	p := newPane("idle")
	p.swallow = true
	if r := promptAndConfirm(t, p); r.outcome != neverStarted {
		t.Fatalf("outcome %q, want %q", r.outcome, neverStarted)
	}
}

// A pane on a rejected model starts the turn, shows the error and is idle
// again about a second later.
func TestConfirmStartCatchesATurnThatEndsAtOnce(t *testing.T) {
	for name, endAfter := range map[string]int{
		"seen working, then settled":      3,
		"started and ended between reads": 1,
	} {
		t.Run(name, func(t *testing.T) {
			p := newPane("idle")
			p.endAfter, p.paneText = endAfter, modelError
			r := promptAndConfirm(t, p)
			if r.outcome != endedAtOnce {
				t.Fatalf("outcome %q, want %q", r.outcome, endedAtOnce)
			}
			if r.paneText != modelError {
				t.Errorf("pane text %q, want the pane's last lines", r.paneText)
			}
		})
	}
}

func TestHerdrSendTreatsATurnThatEndsAtOnceAsNotSent(t *testing.T) {
	for _, kind := range []string{"builder_dispatch", "review_prompt"} {
		p := newPane("idle")
		p.endAfter, p.paneText = 3, modelError
		_, err := transportFor(p).Send(context.Background(), delivery(kind, "builder"))
		if !runner.IsNotSent(err) {
			t.Fatalf("%s: a turn that did no work was not sent: %v", kind, err)
		}
		if !strings.Contains(err.Error(), modelError) {
			t.Errorf("%s: the error should carry what the pane shows: %v", kind, err)
		}
		if len(p.prompts) != 1 {
			t.Errorf("%s: typed %d times", kind, len(p.prompts))
		}
	}
}

// promptAsRC runs `tandem agent prompt --as task` against p.
func promptAsRC(t *testing.T, p *fakePane) (stdout, stderr string, rc int) {
	t.Helper()
	var errb strings.Builder
	stdout, rc = captureStdoutRC(t, func() int { return promptAs(p.deps(), "builder", "PROMPT", deliverPolicy{confirm: true}, &errb) })
	return stdout, errb.String(), rc
}

func TestPromptAsReportsATurnThatIsWorking(t *testing.T) {
	p := newPane("idle")
	out, errs, rc := promptAsRC(t, p)
	if rc != exitcode.OK || out != "started\tworking\n" || errs != "" {
		t.Fatalf("rc %d stdout %q stderr %q", rc, out, errs)
	}
	if len(p.prompts) != 1 {
		t.Errorf("typed %d times", len(p.prompts))
	}
}

func TestPromptAsReportsAPromptThatNeverStarted(t *testing.T) {
	p := newPane("done")
	p.swallow = true
	out, errs, rc := promptAsRC(t, p)
	if rc != exitcode.NeverStarted || out != "" || !strings.HasPrefix(errs, "agent_never_started\t") {
		t.Fatalf("rc %d stdout %q stderr %q", rc, out, errs)
	}
}

func TestPromptAsReportsATurnThatEndedAtOnce(t *testing.T) {
	p := newPane("idle")
	p.endAfter, p.paneText = 3, modelError
	out, errs, rc := promptAsRC(t, p)
	if rc != exitcode.TurnEndedAtOnce || out != "" {
		t.Fatalf("rc %d stdout %q stderr %q", rc, out, errs)
	}
	// The first line is code<TAB>message for the scripts; the pane follows.
	first, rest, _ := strings.Cut(errs, "\n")
	if !strings.HasPrefix(first, "agent_turn_failed\t") || !strings.Contains(rest, modelError) {
		t.Errorf("stderr %q", errs)
	}
}

// The same on the legacy path for Codex: the prompt carries no nonce and Herdr
// names no Codex conversation, so the delivery adds a marker line and finds
// the conversation in Codex's logs by it.
func TestPromptAsFindsACodexTurnByItsDeliveryMarker(t *testing.T) {
	p := newPane("idle")
	p.kind, p.session, p.logged, p.endAfter, p.paneText = "codex", "another-panes", "c9", 3, modelError
	p.tokenLog = map[string]int64{"c9": 0}
	_, errs, rc := promptAsRC(t, p)
	if rc != exitcode.TurnEndedAtOnce {
		t.Fatalf("rc %d stderr %q: a Codex turn that used no tokens counted as started", rc, errs)
	}
	if p.searched == "" || !strings.HasSuffix(p.prompts[0], "(tandem delivery "+p.searched+")") {
		t.Errorf("typed %q, searched by %q: the marker must end the prompt it finds", p.prompts[0], p.searched)
	}
	// A Claude prompt is typed as given: Herdr names its conversation.
	q := newPane("idle")
	promptAsRC(t, q)
	if strings.Contains(q.prompts[0], "tandem delivery") {
		t.Errorf("a Claude prompt was given a marker: %q", q.prompts[0])
	}
}

// A prompt typed into a running turn queues behind it, so the status proves
// nothing either way and is not watched.
func TestPromptAsDoesNotWatchAQueuedPrompt(t *testing.T) {
	p := newPane("working")
	start := p.now
	out, errs, rc := promptAsRC(t, p)
	if rc != exitcode.OK || out != "queued\tworking\n" || errs != "" {
		t.Fatalf("rc %d stdout %q stderr %q", rc, out, errs)
	}
	if len(p.prompts) != 1 || p.now != start {
		t.Errorf("typed %d times, waited %v", len(p.prompts), p.now.Sub(start))
	}
}

func TestPromptAsTypesNothingIntoAPaneItCannotFind(t *testing.T) {
	p := newPane("idle")
	p.getErr = &herdr.Error{Code: "agent_not_found", Message: "no agent named builder"}
	_, errs, rc := promptAsRC(t, p)
	if rc != exitcode.HerdrError || len(p.prompts) != 0 || !strings.HasPrefix(errs, "agent_not_found") {
		t.Fatalf("rc %d prompts %q stderr %q", rc, p.prompts, errs)
	}
}

// A prompt Herdr refused was not typed; its code reaches the scripts as it
// does without --as.
func TestPromptAsPassesOnARefusedPrompt(t *testing.T) {
	p := newPane("idle")
	p.promptErr = &herdr.Error{Code: "agent_blocked", Message: "agent is blocked"}
	_, errs, rc := promptAsRC(t, p)
	if rc != exitcode.HerdrError || !strings.HasPrefix(errs, "agent_blocked") {
		t.Fatalf("rc %d stderr %q", rc, errs)
	}
}

// TANDEM_FAIL_WITHIN_MS shortens the wait for the shell tests. Anything that
// is not a positive whole number keeps the real 8 seconds.
func TestFailWithinComesFromTheEnvironment(t *testing.T) {
	for env, want := range map[string]time.Duration{
		"":     failWithin,
		"1000": time.Second,
		"0":    failWithin,
		"-5":   failWithin,
		"soon": failWithin,
	} {
		t.Setenv("TANDEM_FAIL_WITHIN_MS", env)
		if got := liveHerdrDeps(transportSpec{}).failWithin; got != want {
			t.Errorf("%q: %v, want %v", env, got, want)
		}
	}
}

func TestConfirmStartWaitsTheConfiguredTime(t *testing.T) {
	p := newPane("idle")
	d := p.deps()
	d.failWithin = time.Second
	start := p.now
	w, err := d.watch("builder")
	if err != nil {
		t.Fatal(err)
	}
	a := w.Snapshot("builder")
	mark := markTokens(d, a)
	_ = d.prompt("builder", "PROMPT")
	if r := confirmStart(d, w, "builder", a, mark); r.outcome != started {
		t.Fatalf("outcome %q", r.outcome)
	}
	if waited := p.now.Sub(start); waited >= failWithin {
		t.Errorf("waited %v with failWithin set to 1s", waited)
	}
}

// Herdr pads each pane line to the pane's width. The padding is dropped, so a
// quoted line does not wrap in the planner's terminal.
func TestPaneTailDropsTheLinePadding(t *testing.T) {
	p := newPane("idle")
	p.paneText = "\n⏺ " + modelError + "      \n✻ Brewed for 1s     \n\n"
	if got, want := paneTail(p.deps(), "builder"), "⏺ "+modelError+"\n✻ Brewed for 1s"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// Time alone cannot tell a failed turn from a short one. A turn that ends
// within failWithin failed only if its log shows no model work: a Claude API
// error writes zero usage, and a Codex error no token count.
func TestConfirmStartCountsAShortTurnThatUsedTokens(t *testing.T) {
	p := newPane("idle")
	p.endAfter, p.paneText, p.promptTokens = 3, "Worked for 5s", 1200
	if r := promptAndConfirm(t, p); r.outcome != started {
		t.Fatalf("a 5-second turn that used tokens: outcome %q, want %q", r.outcome, started)
	}
}

// A log tandem cannot read says nothing either way. The send counts: wrongly
// blocking real work costs more than a late warning, and a builder that did
// fail is still reported when its run pauses.
func TestConfirmStartCountsAShortTurnWhoseLogCannotBeRead(t *testing.T) {
	p := newPane("idle")
	p.endAfter, p.unreadable = 3, true
	if r := promptAndConfirm(t, p); r.outcome != started {
		t.Fatalf("outcome %q, want %q", r.outcome, started)
	}
}

// After a reset the turn is in a new conversation, so the old one's tokens are
// not the baseline: a rejected model's turn in the new conversation used none.
func TestConfirmStartComparesTheConversationTheTurnRanIn(t *testing.T) {
	p := newPane("idle")
	p.tokenLog = map[string]int64{"s1": 90000}
	p.newSession, p.resetTok, p.endAfter, p.paneText = "s2", "reset", 3, modelError
	_, err := transportFor(p).Send(context.Background(), delivery("builder_dispatch", "builder"))
	if !runner.IsNotSent(err) {
		t.Fatalf("a new conversation that used no tokens: %v", err)
	}
	q := newPane("idle")
	q.tokenLog = map[string]int64{"s1": 90000}
	q.newSession, q.resetTok, q.endAfter, q.promptTokens = "s2", "reset", 3, 800
	if _, err := transportFor(q).Send(context.Background(), delivery("builder_dispatch", "builder")); err != nil {
		t.Fatalf("a new conversation that used tokens: %v", err)
	}
}

// The live reader sums a conversation's input and output tokens from its log.
// A rejected-model turn leaves one message with zero usage.
func TestConversationTokensReadsTheConversationsLog(t *testing.T) {
	projects := t.TempDir()
	session := "e3e07299-65df-4cfd-9924-94a79f71fc1c"
	log := filepath.Join(projects, "-repo", session+".jsonl")
	if err := os.MkdirAll(filepath.Dir(log), 0o755); err != nil {
		t.Fatal(err)
	}
	failed := `{"type":"assistant","uuid":"u1","sessionId":"` + session + `","isApiErrorMessage":true,"error":"model_not_found",` +
		`"message":{"id":"m1","model":"<synthetic>","stop_reason":"stop_sequence","usage":{"input_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":0}}}`
	if err := os.WriteFile(log, []byte(failed+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	read := conversationTokens(controller.AgentUsageSource{CodexSessions: t.TempDir(), ClaudeProjects: projects})
	if total, ok := read("claude", session); !ok || total != 0 {
		t.Fatalf("a failed turn: %d %v", total, ok)
	}
	real := `{"type":"assistant","uuid":"u2","sessionId":"` + session + `","message":{"id":"m2","model":"claude-sonnet-5-5","stop_reason":"end_turn",` +
		`"usage":{"input_tokens":2,"cache_creation_input_tokens":100,"cache_read_input_tokens":1000,"output_tokens":40}}}`
	if err := os.WriteFile(log, []byte(failed+"\n"+real+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if total, ok := read("claude", session); !ok || total != 1142 {
		t.Fatalf("a real turn: %d %v", total, ok)
	}
	if _, ok := read("claude", "no-such-session"); ok {
		t.Error("a missing log was read")
	}
	if _, ok := read("opencode", session); ok {
		t.Error("a kind with no log reader was read")
	}
}

const daemonError = "Error: Cannot use the shared background server: This session requires api_key_model_discovery to be disabled."

// An agent counts as running only if, for the whole watch, Herdr still lists
// it and its pane is not back at the shell.
func TestAgentAliveCatchesACLIThatQuitsAtLaunch(t *testing.T) {
	for name, p := range map[string]*fakePane{
		"Herdr stops listing it": {status: "idle", goneAfter: 4, paneText: daemonError},
		"its pane is a shell":    {status: "idle", shellAfter: 4, paneText: daemonError},
	} {
		p.now = time.Unix(1_700_000_000, 0)
		alive, why, text := agentAlive(p.deps(), "reviewer", 8*time.Second)
		if alive || why == "" || text != daemonError {
			t.Errorf("%s: alive %v, why %q, pane %q", name, alive, why, text)
		}
	}
	p := newPane("idle")
	if alive, why, _ := agentAlive(p.deps(), "reviewer", 8*time.Second); !alive {
		t.Errorf("an agent that keeps running was reported gone: %s", why)
	}
	if waited := p.now.Sub(time.Unix(1_700_000_000, 0)); waited < 8*time.Second {
		t.Errorf("watched for %v, want the whole window", waited)
	}
}

func TestAgentAliveReturnsWhenTheAgentIsReleased(t *testing.T) {
	p := newPane("idle")
	p.goneAfter = 4 // released on the 4th read, 2 s in
	start := p.now
	alive, why, _ := agentAlive(p.deps(), "builder", failWithin)
	if alive || !strings.Contains(why, "quit") {
		t.Fatalf("alive=%v why=%q", alive, why)
	}
	if p.now.Sub(start) >= failWithin {
		t.Error("watched the whole window after the agent left")
	}
}

func TestAgentAliveChecksTheShellOnceAtTheDeadline(t *testing.T) {
	p := newPane("idle")
	p.shellAfter = 1 // Herdr never released it, but its pane is back at the shell
	if alive, _, _ := agentAlive(p.deps(), "builder", 2*time.Second); alive {
		t.Fatal("alive, want the closing read to see the shell")
	}
}

func TestAgentAliveCommandExitsWithTheReasonAndThePane(t *testing.T) {
	p := newPane("idle")
	p.goneAfter, p.paneText = 3, daemonError
	var errb strings.Builder
	rc := agentAliveCmd(p.deps(), "reviewer", 8*time.Second, &errb)
	first, rest, _ := strings.Cut(errb.String(), "\n")
	if rc != exitcode.AgentExited || !strings.HasPrefix(first, "agent_exited\t") || !strings.Contains(rest, daemonError) {
		t.Fatalf("rc %d stderr %q", rc, errb.String())
	}
	if rc := agentAliveCmd(newPane("idle").deps(), "reviewer", 8*time.Second, &errb); rc != exitcode.OK {
		t.Errorf("a running agent: rc %d", rc)
	}
}

// A Codex conversation whose only turn failed at the API has a rollout but no
// token_count events. For usage that is unknown; for a turn that ended at once
// it is the answer: the model did no work, so the total is 0.
func TestConversationTokensReadsACodexLogWithNoTokenEventsAsZero(t *testing.T) {
	sessions := t.TempDir()
	dir := filepath.Join(sessions, "2026", "10", "11")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "rollout-2026-10-11T00-00-00-sess-7.jsonl"), []byte(`{"type":"session_meta","payload":{"id":"sess-7"}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	read := conversationTokens(controller.AgentUsageSource{CodexSessions: sessions, ClaudeProjects: t.TempDir()})
	if total, ok := read("codex", "sess-7"); !ok || total != 0 {
		t.Fatalf("a rollout with no token events: %d %v, want 0 true", total, ok)
	}
	if _, ok := read("codex", "sess-8"); ok {
		t.Error("a session with no rollout was read")
	}
}

func TestPromptAsWaitsOutABlockedAgent(t *testing.T) {
	p := newPane("blocked")
	p.unblockAfter = 2 // the dialog clears on the 2nd read
	if code := promptAs(p.deps(), "builder", "hi", deliverPolicy{confirm: true, wait: 10 * time.Second}, io.Discard); code != exitcode.OK {
		t.Fatalf("exit %d", code)
	}
	if len(p.prompts) != 1 {
		t.Errorf("typed %d prompts, want 1 after the dialog cleared", len(p.prompts))
	}
}

func TestPromptAsRefusesWhenStillBlocked(t *testing.T) {
	p := newPane("blocked")
	var errb bytes.Buffer
	code := promptAs(p.deps(), "builder", "hi", deliverPolicy{confirm: true, wait: 2 * time.Second}, &errb)
	if code != exitcode.HerdrError || !strings.HasPrefix(errb.String(), "agent_blocked\t") {
		t.Fatalf("exit %d stderr %q", code, errb.String())
	}
	if len(p.prompts) != 0 {
		t.Error("typed into a blocked pane")
	}
}

// brokenStream is a watch whose stream fails at once, so the wait falls to its
// closing read.
type brokenStream struct{ eventWatch }

func (b brokenStream) Next(time.Duration) (herdr.Event, bool, error) {
	return herdr.Event{}, false, errors.New("stream broke")
}

// unreachableAfterWatch is p's deps with a stream that breaks and a get that
// fails as an unreachable socket once the watch is open.
func unreachableAfterWatch(p *fakePane) herdrDeps {
	d := p.deps()
	watch, down := d.watch, false
	d.watch = func(names ...string) (eventWatch, error) {
		w, err := watch(names...)
		down = true
		return brokenStream{w}, err
	}
	get := d.get
	d.get = func(name string) (*herdr.Agent, error) {
		if down {
			return nil, &herdr.Error{Code: "unreachable", Message: "no socket", Unreachable: true}
		}
		return get(name)
	}
	return d
}

func TestPromptAsBlockedWaitReportsAnUnreachableSocket(t *testing.T) {
	p := newPane("blocked")
	var errb bytes.Buffer
	code := promptAs(unreachableAfterWatch(p), "builder", "hi", deliverPolicy{confirm: true, wait: 2 * time.Second}, &errb)
	if code != exitcode.Unreachable || strings.HasPrefix(errb.String(), "agent_blocked") {
		t.Fatalf("exit %d stderr %q", code, errb.String())
	}
	if len(p.prompts) != 0 {
		t.Error("typed into a blocked pane")
	}
}

// --as does its own waiting, so a herdr wait flag beside it is refused before
// anything reaches Herdr.
func TestPromptAsRefusesHerdrWaitFlags(t *testing.T) {
	for _, extra := range [][]string{{"--wait"}, {"--until", "idle"}, {"--timeout", "5000"}} {
		args := append([]string{"prompt", "builder", "PROMPT", "--as", "task"}, extra...)
		if rc := cmdAgent(args); rc != exitcode.Usage {
			t.Errorf("%v: rc %d, want usage", extra, rc)
		}
	}
}

func TestPromptAsRefusesBadFlags(t *testing.T) {
	for _, args := range [][]string{
		{"prompt", "builder", "P", "--as", "fast"},
		{"prompt", "builder", "P", "--wait-unblocked", "500"},
		{"prompt", "builder", "P", "--as", "handback", "--wait-unblocked", "-1"},
	} {
		if rc := cmdAgent(args); rc != exitcode.Usage {
			t.Errorf("%v: rc %d, want usage", args, rc)
		}
	}
}

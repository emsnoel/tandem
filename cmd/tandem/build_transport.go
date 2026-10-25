package main

// The Herdr transport of the durable engine: how the controller's prompts reach
// a pane and how pane state is read back.
//
// One transport serves every builder kind; the reset command and confirm key
// come from the agent registry. Herdr shows status, a lifecycle counter and a
// session id, never a nonce, so an effect in doubt is resolved only by the
// builder returning the nonce through the mailbox.

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/emsnoel/tandem/internal/agent"
	"github.com/emsnoel/tandem/internal/artifact"
	"github.com/emsnoel/tandem/internal/controller"
	"github.com/emsnoel/tandem/internal/herdr"
	"github.com/emsnoel/tandem/internal/runner"
)

// transportSpec is what a transport needs to know about a run.
type transportSpec struct {
	Kind      string // the builder's agent kind
	Builder   string // the builder's Herdr agent name
	Planner   string // the planner's Herdr agent name
	Root      string // the checkout, where the relay and builder.json live
	Artifacts *artifact.Store
	// ShareContext lets a builder with no verified way to start a fresh
	// conversation be sent its task in the one it has. Without it that builder is
	// never sent a task, because its earlier context is unknown.
	ShareContext bool
	// ReviewerKind and Reviewer name the reviewer pane, when the run has one:
	// review prompts are typed there, never reset, and observed by that name.
	ReviewerKind string
	Reviewer     string
}

// runTransportSpec is the transport spec of a run that already exists, from
// the agents it was started with.
func runTransportSpec(ag controller.Agents, root string, art *artifact.Store) transportSpec {
	return transportSpec{Kind: ag.BuilderKind, Builder: ag.BuilderSession, Planner: ag.PlannerSession, Root: root, Artifacts: art,
		ShareContext: ag.ShareContext, ReviewerKind: ag.ReviewerKind, Reviewer: ag.ReviewerSession}
}

// maxPromptBytes bounds a prompt passed as one command argument.
const maxPromptBytes = 256 << 10

const (
	landWithin = 8 * time.Second
	resetWait  = 15 * time.Second
)

// herdrDeps are the effects the transport performs, injected so the decisions
// can be tested without a Herdr.
type herdrDeps struct {
	get    func(name string) (*herdr.Agent, error)
	prompt func(name, text string) error
	// watch subscribes to the agents' events, then snapshots them (agent_watch.go).
	watch func(names ...string) (eventWatch, error)
	read  func(name string) (string, error)
	// readTail is the last lines of a pane, unwrapped: a review's reply.
	readTail func(name string, lines int) (string, error)
	// screen is what the pane shows now, to tell its main conversation's
	// input from a screen typed text would miss (confirm_view.go).
	screen func(name string) (string, error)
	// shellForeground reports whether a pane's foreground process is its shell,
	// and readPane reads a pane by id, for an agent Herdr no longer lists.
	shellForeground func(pane string) (bool, error)
	readPane        func(pane string) (string, error)
	// tokens is a conversation's token total from its own log, and whether
	// the log could be read (confirm_start.go).
	tokens func(kind, session string) (int64, bool)
	// find is the conversation whose log holds marker, written since since,
	// "" when none is found: how a turn's conversation is read when Herdr names
	// none, as for Codex.
	find func(kind, marker string, since time.Time) string
	// failWithin is how long a started turn must keep running; 0 is the
	// default (confirm_start.go).
	failWithin time.Duration
	// reset resets a pane's conversation: its token, and the new
	// conversation's id when the reset proved one.
	reset      func(name, root string) (tok, session string, err error)
	readPrompt func(ctx context.Context, hash string) (string, error)
	sleep      func(time.Duration)
	now        func() time.Time
}

type herdrTransport struct {
	spec transportSpec
	d    herdrDeps
}

func newHerdrTransport(spec transportSpec, d herdrDeps) *herdrTransport {
	return &herdrTransport{spec: spec, d: d}
}

func liveHerdrDeps(spec transportSpec) herdrDeps {
	return herdrDeps{
		get:   herdr.Get,
		watch: liveWatch,
		prompt: func(name, text string) error {
			_, err := herdr.Raw("agent", "prompt", name, text)
			return err
		},
		read: func(name string) (string, error) {
			b, err := herdr.Raw(readArgs(name, "recent-unwrapped", paneLines)...)
			return string(b), err
		},
		readTail: func(name string, lines int) (string, error) {
			b, err := herdr.Raw(readArgs(name, "recent-unwrapped", lines)...)
			return string(b), err
		},
		screen:     liveScreen,
		failWithin: failWithinFromEnv(),
		tokens:     conversationTokens(controller.DefaultAgentUsageSource()),
		find: func(kind, marker string, since time.Time) string {
			s, _ := controller.DefaultAgentUsageSource().SessionWith(context.Background(), kind, marker, since, time.Now())
			return s
		},
		shellForeground: func(pane string) (bool, error) {
			b, err := herdr.Raw("pane", "process-info", "--pane", pane)
			if err != nil {
				return false, err
			}
			info, err := decodeProcessInfo(b)
			if err != nil {
				return false, fmt.Errorf("pane %s: %w", pane, err)
			}
			return shellReady(info), nil
		},
		readPane: func(pane string) (string, error) {
			b, err := herdr.Raw("pane", "read", pane, "--lines", strconv.Itoa(paneLines))
			return string(b), err
		},
		reset: func(name, root string) (string, string, error) {
			return runReset(liveResetDeps, name, root, resetWait, false)
		},
		readPrompt: func(ctx context.Context, hash string) (string, error) {
			if spec.Artifacts == nil {
				return "", errors.New("no artifact store")
			}
			b, err := spec.Artifacts.ReadAll(ctx, hash, maxPromptBytes+1)
			return string(b), err
		},
		sleep: time.Sleep,
		now:   time.Now,
	}
}

// defaultDurableTransport is the transport `tandem build` ships with. A kind
// with no adapter is refused here, before a run exists, not at the first send.
func defaultDurableTransport(spec transportSpec) (runner.Transport, error) {
	if _, err := agent.Get(spec.Kind); err != nil {
		return nil, fmt.Errorf("no transport for builder kind %q: %w", spec.Kind, err)
	}
	if spec.Builder == "" {
		return nil, errors.New("a durable run needs the builder's agent name")
	}
	if spec.Reviewer == "" || spec.ReviewerKind == "" {
		return nil, errors.New("a durable run needs its reviewer pane's agent name and kind")
	}
	if _, err := agent.Get(spec.ReviewerKind); err != nil {
		return nil, fmt.Errorf("no transport for reviewer kind %q: %w", spec.ReviewerKind, err)
	}
	return newHerdrTransport(spec, liveHerdrDeps(spec)), nil
}

// Send types one effect's prompt into its agent's pane, delivered by the
// effect kind's policy (effectPolicies, deliver.go). A prompt is a delivery
// only once the pane is seen to react. One that changed nothing Herdr can see
// may be sitting in a dialog, so it is reported as possibly delivered and is
// never typed again. A task or review prompt whose turn ends at once did no
// work, so it is reported as not sent.
func (t *herdrTransport) Send(ctx context.Context, d runner.Delivery) (runner.Receipt, error) {
	p, ok := effectPolicies[d.Kind]
	if !ok {
		return runner.Receipt{}, runner.NotSent(fmt.Errorf("effect kind %q is not delivered to an agent", d.Kind))
	}
	name := d.SessionID
	if name == "" {
		return runner.Receipt{}, runner.NotSent(errors.New("the effect names no agent to send to"))
	}
	text, err := t.d.readPrompt(ctx, d.PromptHash)
	if err != nil {
		return runner.Receipt{}, runner.NotSent(fmt.Errorf("reading the prompt: %w", err))
	}
	if len(text) > maxPromptBytes {
		return runner.Receipt{}, runner.NotSent(fmt.Errorf("the prompt is %d bytes; the most one command can carry is %d", len(text), maxPromptBytes))
	}
	res := deliverPrompt(t.d, deliverReq{name: name, text: text, nonce: d.Nonce, root: t.spec.Root, shareContext: t.spec.ShareContext}, p)
	switch res.outcome {
	case sendRefused:
		return runner.Receipt{}, runner.NotSent(res.err)
	case sendTyped, sendQueued:
		return runner.Receipt{EffectID: d.EffectID, ExternalID: externalID(res.agent)}, nil
	case sendStarted:
		conv := res.proved
		if conv == "" {
			conv = t.conversation(name, res.agent, res.before, res.reset)
		}
		return runner.Receipt{EffectID: d.EffectID, ExternalID: conv}, nil
	case sendEndedAtOnce:
		// The turn did no work, so the prompt counts as never sent.
		return runner.Receipt{}, runner.NotSent(fmt.Errorf("%s's turn ended as soon as it started, so nothing is working on it. Its pane shows:\n%s", name, res.paneText))
	case sendNeverStarted:
		return runner.Receipt{}, fmt.Errorf("the prompt was typed into %s but it never started working; it may be at a native dialog Herdr cannot see: check its pane", name)
	default: // sendUncertain: never NotSent, which would let it be typed again
		return runner.Receipt{}, fmt.Errorf("the prompt may have reached %s, but Herdr did not answer: %w", name, res.err)
	}
}

// sessionWithin is how long a receipt waits for Herdr to report the session of
// a conversation that was just started.
const sessionWithin = 6 * time.Second

// conversation names the conversation a prompt was typed into. Herdr names it
// only for Claude panes; other kinds use the id their reset proved, or none,
// and the run finds the conversation by its nonce at handback. A Claude id can
// arrive late, so the old id is never reported as the new one, and a missing
// id after sessionWithin is "", never the pane id.
func (t *herdrTransport) conversation(name string, cur *herdr.Agent, before string, reset bool) string {
	if !reset {
		return externalID(cur)
	}
	if !herdr.NamesConversation(cur.Kind) {
		return ""
	}
	if s := cur.Conversation(); s != "" && s != before {
		return s
	}
	w, err := t.d.watch(name)
	if err != nil {
		return ""
	}
	defer w.Close()
	return awaitSession(t.d, w, name, before, sessionWithin)
}

func externalID(a *herdr.Agent) string {
	if s := a.Conversation(); s != "" {
		return s
	}
	return a.PaneID
}

// Observe reports on the builder's pane. A working or blocked pane is running;
// a settled one is completed, which means nothing is writing. Anything Herdr
// cannot say is unknown. A pane Herdr cannot find is an error, not a stopped
// builder, so a stop never frees the writer on a guess.
func (t *herdrTransport) Observe(ctx context.Context, _ string) (runner.Observation, error) {
	return t.ObserveAgent(ctx, t.spec.Builder)
}

// ReadAgent is the last lines of an agent's pane, unwrapped, so a JSON object
// the CLI printed over several screen lines comes back whole.
func (t *herdrTransport) ReadAgent(_ context.Context, name string, lines int) (string, error) {
	return t.d.readTail(name, lines)
}

// ObserveAgent observes one agent by name: the builder for a task or review
// prompt, the planner for a verification prompt, the reviewer pane for a
// review prompt sent there.
func (t *herdrTransport) ObserveAgent(_ context.Context, name string) (runner.Observation, error) {
	a, err := t.d.get(name)
	if err != nil {
		if herdr.IsGone(err) {
			return runner.Observation{}, fmt.Errorf("%w: %s: %v", runner.ErrAgentGone, name, err)
		}
		return runner.Observation{}, err
	}
	obs := runner.Observation{State: runner.ObsUnknown, ExternalID: externalID(a), Session: a.Conversation()}
	switch st := a.StatusString(); {
	case st == "working" || st == "blocked":
		obs.State = runner.ObsRunning
	case agent.Settled(st):
		obs.State = runner.ObsCompleted
	}
	return obs, nil
}

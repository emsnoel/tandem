package main

// tandem agent reset gives a pane an empty conversation by typing its CLI's
// reset command, so a build does not carry the review's context. It prints one
// token for bin/tandem-handoff to explain, and never touches builder.json.

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/emsnoel/tandem/internal/agent"
	"github.com/emsnoel/tandem/internal/config"
	"github.com/emsnoel/tandem/internal/controller"
	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/herdr"
	"github.com/emsnoel/tandem/internal/paths"
)

type resetDeps struct {
	command func(kind string) string
	get     func(name string) (*herdr.Agent, error)
	watch   func(names ...string) (eventWatch, error)
	submit  func(name, text string) error
	press   func(name, key string) error
	screen  func(name string) (string, error)
	// fresh reads the kind's own records for a conversation started at or
	// after since. measured is false for a kind with no such record.
	fresh func(kind string, a *herdr.Agent, since time.Time) (session string, measured bool)
	now   func() time.Time
}

var liveResetDeps = resetDeps{
	command: func(kind string) string {
		a, err := agent.Get(kind)
		if err != nil {
			return ""
		}
		return a.ResetCommand()
	},
	get:   herdr.Get,
	watch: liveWatch,
	submit: func(name, text string) error {
		_, err := herdr.Raw(resetSubmitArgs(name, text)...)
		return err
	},
	press: func(name, key string) error {
		_, err := herdr.Raw("agent", "send-keys", name, key)
		return err
	},
	screen: liveScreen,
	fresh: func(kind string, a *herdr.Agent, since time.Time) (string, bool) {
		switch kind {
		case "copilot":
			return copilotFreshSession(controller.DefaultCopilotHome(), a.PaneID, since), true
		case "agy":
			return agyFreshSession(paths.AgyHome(), a.PaneID, since), true
		}
		return "", false
	},
	now: time.Now,
}

// resetSubmitArgs types the command and presses Enter. It omits --wait because
// a reset may never report `working` long enough for --wait to see it.
func resetSubmitArgs(name, text string) []string {
	return []string{"agent", "prompt", name, text}
}

// resetPoll is how often the pane's screen and the kind's own record are
// read. Herdr sends no event when either changes.
const resetPoll = 500 * time.Millisecond

// resetKind is the recorded kind of the pane named name: reviewer.json's when
// it names that pane, builder.json's otherwise. An unreadable reviewer.json
// gives "", since the wrong kind would type the wrong command.
func resetKind(root, name string) string {
	rec, err := config.LoadReviewer(paths.Relay(root))
	if err != nil && !errors.Is(err, config.ErrNoReviewer) {
		return ""
	}
	if err == nil && rec.Name == name {
		return rec.Kind
	}
	kind, _ := builderIdentity(root)
	return kind
}

// runReset resets name's conversation and returns its token, and the new
// conversation's id when the reset proved one.
func runReset(d resetDeps, name, root string, timeout time.Duration, dry bool) (tok, session string, err error) {
	kind := resetKind(root, name)
	if kind == "" {
		return "skip:no-identity", "", nil
	}
	cmd := d.command(kind)
	w, err := d.watch(name)
	if err != nil {
		return "", "", err
	}
	defer w.Close()
	a := w.Snapshot(name)
	last, _ := os.ReadFile(filepath.Join(paths.Relay(root), "build-session"))
	// Only Claude's report names the pane's conversation. A Codex pane may hold
	// one that Herdr credits to another pane.
	before := a.Conversation()
	if tok := agent.ResetDecision(cmd, a.StatusString(), a.HasNoConversation(), before, strings.TrimSpace(string(last))); tok != "go" {
		return tok, "", nil
	}
	// A pane showing a subagent or a picker would take the command elsewhere.
	if _, off := offMain(d.screen, kind, name); off {
		return "skip:off-main", "", nil
	}
	// A pane whose turn ended with a background task still running is not idle.
	if _, busy := busyInBackground(d.screen, kind, name); busy {
		return "skip:not-idle", "", nil
	}
	if dry {
		return "would-reset", "", nil
	}
	since := d.now()
	if err := d.submit(name, cmd); err != nil {
		return "", "", err
	}
	// A reset is proven by change, not time: a moved conversation id ("reset")
	// or a status event since the command ("sent"). A kind with its own record
	// (Copilot's or agy's process log) is judged by that alone. A dialog the command
	// opened holds the new conversation until it is answered, while Herdr may
	// report idle or a status change, so the screen is read before any of
	// that counts. Only the kind's own reset dialog is answered, once.
	moved, answered, measured := false, false, false
	lastStatus := a.StatusString()
	deadline := d.now().Add(timeout)
	for d.now().Before(deadline) {
		ev, ok, err := w.Next(min(deadline.Sub(d.now()), resetPoll))
		if err != nil {
			break
		}
		// Only a status change proves a lifecycle ran. pane_updated also fires
		// for titles and token counts.
		if ok && ev.Name == name && ev.Status != "" && (ev.Kind == "pane_agent_status_changed" || ev.Status != lastStatus) {
			moved = true
		}
		if ok && ev.Name == name && ev.Status != "" {
			lastStatus = ev.Status
		}
		if !answered {
			if key := resetDialogKey(d.screen, kind, name); key != "" {
				answered = true
				if err := d.press(name, key); err != nil {
					return "", "", err
				}
				// Whatever moved before the answer was not the new conversation.
				moved = false
				continue
			}
		}
		b, gerr := d.get(name)
		if gerr != nil {
			continue
		}
		settled := agent.Settled(b.StatusString())
		if settled {
			if _, busy := busyInBackground(d.screen, kind, name); busy {
				settled = false
			}
		}
		var s string
		if s, measured = d.fresh(kind, b, since); measured {
			if s != "" && settled {
				return "reset", s, nil
			}
			// agy's process log may not flush before the reset timeout, and
			// /new on an already-fresh prompt (e.g. --resume) creates no new
			// conversation. A visibly fresh screen confirms the reset.
			if settled && freshScreen(d.screen, kind, name) {
				return "sent", "", nil
			}
			continue
		}
		if settled {
			if s := b.Conversation(); s != "" && s != before {
				return "reset", s, nil
			}
			if moved {
				return "sent", "", nil
			}
		}
	}
	return "unconfirmed", "", nil
}

// resetDialogKey reads name's screen once and returns the key that answers
// kind's reset dialog there, or "" (agent.ResetDialogKey). A screen that
// cannot be read gets no key.
func resetDialogKey(screen func(string) (string, error), kind, name string) string {
	if screen == nil {
		return ""
	}
	text, err := screen(name)
	if err != nil {
		return ""
	}
	return agent.ResetDialogKey(kind, text)
}

// runSession returns the conversation Herdr reports for name. With not set, it
// waits up to timeout for an id other than not, then returns the current one.
func runSession(d herdrDeps, name, not string, timeout time.Duration) (string, error) {
	a, err := d.get(name)
	if err != nil {
		return "", err
	}
	if not == "" || !herdr.NamesConversation(a.Kind) {
		return a.Conversation(), nil
	}
	if s := a.Conversation(); s != "" && s != not {
		return s, nil
	}
	w, err := d.watch(name)
	if err != nil {
		return "", err
	}
	defer w.Close()
	if s := awaitSession(d, w, name, not, timeout); s != "" {
		return s, nil
	}
	if cur, err := d.get(name); err == nil {
		return cur.Conversation(), nil
	}
	return a.Conversation(), nil
}

func cmdAgentReset(args []string) int {
	const use = "usage: tandem agent reset NAME --root DIR [--timeout MS] [--dry-run]"
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(os.Stderr, use)
		return exitcode.Usage
	}
	name := args[0]
	fs := flag.NewFlagSet("agent reset", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	root := fs.String("root", "", "repository root (where the relay mailbox lives)")
	timeoutMS := fs.Int("timeout", 15000, "how long to wait for the builder to settle, in ms")
	dry := fs.Bool("dry-run", false, "decide and report, send nothing")
	if err := fs.Parse(args[1:]); err != nil || *root == "" {
		fmt.Fprintln(os.Stderr, use)
		return exitcode.Usage
	}
	tok, _, err := runReset(liveResetDeps, name, *root, time.Duration(*timeoutMS)*time.Millisecond, *dry)
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return herdr.ExitCodeFor(err)
	}
	fmt.Println(tok)
	return exitcode.OK
}

// cmdAgentSession prints the session Herdr reports for NAME; nothing when none.
func cmdAgentSession(args []string) int {
	const use = "usage: tandem agent session NAME [--not ID] [--timeout MS]"
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(os.Stderr, use)
		return exitcode.Usage
	}
	name := args[0]
	fs := flag.NewFlagSet("agent session", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	not := fs.String("not", "", "wait for a session other than this one")
	timeoutMS := fs.Int("timeout", 5000, "how long --not waits, in ms")
	if err := fs.Parse(args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, use)
		return exitcode.Usage
	}
	s, err := runSession(liveHerdrDeps(transportSpec{}), name, *not, time.Duration(*timeoutMS)*time.Millisecond)
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return herdr.ExitCodeFor(err)
	}
	if s != "" {
		fmt.Println(s)
	}
	return exitcode.OK
}

// cmdAgentExplainReset prints the sentence for one `tandem agent reset` token.
// TOKEN may be empty.
func cmdAgentExplainReset(args []string) int {
	const use = "usage: tandem agent explain-reset TOKEN [--for build|review] [--who builder|reviewer]"
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(os.Stderr, use)
		return exitcode.Usage
	}
	fs := flag.NewFlagSet("agent explain-reset", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	purpose := fs.String("for", "build", "what the fresh conversation is for: build or review")
	who := fs.String("who", "builder", "the role of the pane that was reset: builder or reviewer")
	if err := fs.Parse(args[1:]); err != nil ||
		(*purpose != "build" && *purpose != "review") || (*who != "builder" && *who != "reviewer") {
		fmt.Fprintln(os.Stderr, use)
		return exitcode.Usage
	}
	fmt.Println(agent.ResetSentence(args[0], *purpose, *who))
	return exitcode.OK
}

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/emsnoel/tandem/internal/agent"
	"github.com/emsnoel/tandem/internal/config"
	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/herdr"
	"github.com/emsnoel/tandem/internal/state"
)

// plannerPlaces are what a Codex planner's sandbox must reach beyond its
// checkout for tandem's commands to work from its shell.
type plannerPlaces struct {
	// Socket is Herdr's socket, which every tandem command talks to.
	Socket string
	// StateHome holds every checkout's durable run state.
	StateHome string
	// GitDir is the checkout's common .git directory, which Codex's
	// :workspace keeps read-only. tandem-pair writes info/exclude there, and
	// the planner commits. Empty outside a git checkout.
	GitDir string
}

// codexPlannerSpec is the Spec a Codex planner starts with. A person sits at
// the planner, so it defaults to ask: Codex asks before anything its sandbox
// denies, and tandem's own commands run without asking.
func codexPlannerSpec(r config.Resolved, cwd string, p plannerPlaces) agent.Spec {
	if r.Autonomy.Value == "" {
		r.Autonomy = config.Value{Value: string(agent.Ask), From: config.FromDefault}
	}
	s := r.Spec(cwd)
	s.HerdrSocket = p.Socket
	for _, d := range []string{p.StateHome, p.GitDir} {
		if d != "" {
			s.Sandbox.WritableDirs = append(s.Sandbox.WritableDirs, d)
		}
	}
	return s
}

// cmdCodex starts Codex in this pane as a tandem planner. Codex's sandbox
// denies Herdr's socket, so a Codex started plainly cannot run tandem-pair;
// this adds the socket, the state home and the git directory to its
// permission profile, then hands the process to Codex. Arguments are passed
// on to Codex after tandem's own.
func cmdCodex(args []string) int {
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
		fmt.Fprintln(os.Stderr, "usage: tandem codex [CODEX ARGS...]\n\nStart Codex in this Herdr pane as a tandem planner, with Herdr's socket in its sandbox.")
		return exitcode.OK
	}
	user, err := config.LoadUser(config.UserConfigPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "tandem codex: %v\n", err)
		return exitcode.Usage
	}
	r := config.ResolveRole(agent.RolePlanner, &config.Repo{}, user, os.Getenv, config.Overrides{Kind: "codex"})
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "tandem codex: %v\n", err)
		return exitcode.Internal
	}

	sock, err := herdr.SocketPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "tandem codex: Herdr's socket is unknown (%v). Start Herdr, then run tandem codex from a Herdr pane.\n", err)
		return herdr.ExitCodeFor(err)
	}
	home, err := state.Home(os.Getenv("TANDEM_STATE_HOME"))
	if err == nil {
		// Codex cannot grant a directory that does not exist yet.
		err = os.MkdirAll(home, 0o700)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "tandem codex: the state home: %v\n", err)
		return exitcode.Internal
	}
	gitDir, _ := state.GitCommonDir(cwd) // none outside a checkout
	if os.Getenv("HERDR_PANE_ID") == "" {
		fmt.Fprintln(os.Stderr, "tandem codex: warning: this shell is not in a Herdr pane, so tandem-pair will not find this Codex as the planner")
	}

	a, err := agent.Get("codex")
	if err != nil {
		fmt.Fprintf(os.Stderr, "tandem codex: %v\n", err)
		return exitcode.Internal
	}
	l, err := a.Resolve(codexPlannerSpec(r, cwd, plannerPlaces{Socket: filepath.Clean(sock), StateHome: home, GitDir: gitDir}))
	if err != nil {
		return refuseRemedy(err)
	}
	for _, w := range l.Warnings {
		fmt.Fprintf(os.Stderr, "tandem codex: %s=%s -> %s (%s)\n", w.Axis, w.Requested, w.Substituted, w.DirectionS)
	}
	bin := r.Path
	if bin == "" {
		if bin, err = exec.LookPath("codex"); err != nil {
			fmt.Fprintln(os.Stderr, "tandem codex: codex is not on PATH")
			return exitcode.Usage
		}
	}
	argv := append(append([]string{"codex"}, l.Args...), args...)
	err = syscall.Exec(bin, argv, os.Environ())
	// Only reached if the exec itself failed.
	fmt.Fprintf(os.Stderr, "tandem codex: could not run %s: %v\n", bin, err)
	return exitcode.Internal
}

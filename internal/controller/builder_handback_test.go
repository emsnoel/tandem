package controller

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/agent"
	"github.com/emsnoel/tandem/internal/paths"
	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/taskcontext"
	"github.com/emsnoel/tandem/internal/workflow"
)

func TestBuilderHandbackReducesPacketBytes(t *testing.T) {
	t.Parallel()
	for _, root := range []string{"/repo", "/checkout/" + strings.Repeat("a long directory/", 14) + "builder's repo"} {
		cfg := runConfig{Root: root, PlanPath: filepath.Join(root, "plan.md"), CheckCommand: "./tests/run.sh"}
		id, hash := strings.Repeat("a", 32), strings.Repeat("a", 64)
		spec := workflow.TaskSpec{ID: "task-1", Brief: "Update the greeting."}
		render := func(cfg runConfig) string {
			t.Helper()
			p, err := taskcontext.Build(packetRequest(cfg, id, hash, hash, id, 1, 1, spec, nil, ""))
			if err != nil {
				t.Fatal(err)
			}
			return p.Prompt
		}
		// Runs without a helper use the same long-form footer as 711ed6d.
		baseline := render(cfg)
		cfg.BuilderHandbackRelay = ".tandem-relay"
		prompt := render(cfg)
		p := attemptIdentity(root, id, id, 1)
		shortCommand := shellQuote(builderHandbackPath(cfg, id, p.Nonce))
		longCommand := p.handbackCommand()
		if strings.Count(prompt, shortCommand) != 2 || strings.ReplaceAll(prompt, shortCommand, longCommand) != baseline {
			t.Fatal("expected only the two handback invocations to change")
		}
		if len(prompt) >= len(baseline) {
			t.Fatalf("helper packet is %d bytes, baseline is %d bytes", len(prompt), len(baseline))
		}
		t.Logf("%d-byte root: %d -> %d prompt bytes", len(root), len(baseline), len(prompt))
	}
}

func TestGeneratedBuilderHandbackEndToEnd(t *testing.T) {
	t.Parallel()
	bin := handbackTestBin(t)
	e := ownedEnv(t)
	quotedRoot := filepath.Join(filepath.Dir(e.root), "builder's repo")
	if err := os.Rename(e.root, quotedRoot); err != nil {
		t.Fatal(err)
	}
	quotedRoot, err := filepath.EvalSymlinks(quotedRoot)
	if err != nil {
		t.Fatal(err)
	}
	e.root, e.relay, e.plan = quotedRoot, paths.Relay(quotedRoot), filepath.Join(quotedRoot, "plan.md")
	e.startInCollaboration()
	e.bindRecoveryWork("task-1")
	e.tick()
	first := e.current("task-1")
	firstEffect := e.builderEffect(first)
	prompt, _ := e.packet(first)
	firstCommand := shellQuote(filepath.Join(RunDir(e.relay, e.runID), "builder", firstEffect.Nonce))
	if !strings.Contains(prompt, firstCommand+" --started") {
		t.Fatalf("prompt lacks the quoted invocation %s", firstCommand)
	}
	elsewhere := t.TempDir()
	report := "builder's report.md"
	body := "Implemented the first change.\n"
	write(t, elsewhere, report, body)
	invoke := func(command, args string) {
		t.Helper()
		// Run the printed command through a shell to exercise its quoting.
		script := "cd -- " + shellQuote(elsewhere) + " && " + command + " " + args
		cmd := exec.Command("bash", "-c", script)
		cmd.Dir = e.root
		if os.Getenv("TANDEM_TEST_BUILDER_SANDBOX") == "1" {
			codex, err := agent.Get("codex")
			if err != nil {
				t.Fatal(err)
			}
			launch, err := codex.Resolve(agent.Spec{Role: agent.RoleBuilder, Autonomy: agent.Auto, Cwd: e.root})
			if err != nil {
				t.Fatal(err)
			}
			sandbox := []string{"sandbox", "--cd", e.root}
			for i := 0; i+1 < len(launch.Args); i++ {
				if launch.Args[i] == "-c" {
					if profile, ok := strings.CutPrefix(launch.Args[i+1], `default_permissions="`); ok {
						sandbox = append(sandbox, "--permission-profile", strings.TrimSuffix(profile, `"`))
					} else {
						sandbox = append(sandbox, launch.Args[i:i+2]...)
					}
					i++
				}
			}
			cmd = exec.Command("codex", append(sandbox, "--", "bash", "-c", script)...)
			cmd.Dir = e.root
		}
		cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v\n%s", args, err, out)
		}
	}

	invoke(firstCommand, "--started")
	e.tick()
	invoke(firstCommand, "--started")
	e.tick()
	if got := e.eventCount(workflow.EventBuilderStarted); got != 1 {
		t.Fatalf("duplicate acknowledgement produced %d start events", got)
	}
	e.restart()
	e.settle(first)
	invoke(firstCommand, "--report "+shellQuote(report)+" --stuck")
	e.tick()
	first = e.current("task-1")
	if first.Implementation != workflow.ImplementationStuck || e.eventCount(workflow.EventBuilderStuck) != 1 {
		t.Fatalf("stuck report did not apply: %s", first.Implementation)
	}
	second, err := e.successorOf(first, state.CauseStuck)
	if err != nil {
		t.Fatal(err)
	}
	e.tick()
	invoke(firstCommand, "--started")
	e.tick()
	if cur := e.current("task-1"); cur.ID != second.ID || cur.Implementation != workflow.ImplementationPending {
		t.Fatalf("stale helper changed the successor: %+v", cur)
	}
	secondCommand := shellQuote(filepath.Join(RunDir(e.relay, e.runID), "builder", e.builderEffect(second).Nonce))
	invoke(secondCommand, "--started")
	e.tick()
	e.restart()
	e.settle(second)
	invoke(secondCommand, "--report "+shellQuote(report))
	e.tick()
	if got := e.eventCount(workflow.EventBuilderSubmitted); got != 1 {
		t.Fatalf("completed report applied %d times", got)
	}
	for _, kind := range []string{workflow.EventBuilderStuck, workflow.EventBuilderSubmitted} {
		if got := e.countWhere("SELECT count(*) FROM events WHERE kind=? AND payload_hash=?", kind, sha(body)); got != 1 {
			t.Fatalf("%s did not preserve the relative report's content", kind)
		}
	}
	if sent := e.delivered()[workflow.EffectBuilderDispatch]; sent != 2 {
		t.Fatalf("%d builder deliveries for two attempts", sent)
	}
}

func TestBuilderHandbackFailureBlocksDelivery(t *testing.T) {
	t.Parallel()
	e := newCEnv(t)
	e.start()
	eff := e.builderEffect(e.current("task-1"))
	dir := filepath.Join(RunDir(e.relay, e.runID), "builder")
	if err := os.WriteFile(dir, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := e.c.Tick(ctx, e.runID); err == nil || !strings.Contains(err.Error(), "prepare builder handback") {
		t.Fatalf("expected helper creation failure, got %v", err)
	}
	if len(e.tr.sends) != 0 {
		t.Fatal("sent a prompt with an unusable helper")
	}
	if reason, found, err := e.st.EffectNotSentReason(ctx, eff.ID); err != nil || !found || !strings.Contains(e.readArtifact(reason), "prepare builder handback") {
		t.Fatalf("delivery failure not recorded: %q, %t, %v", reason, found, err)
	}
}

package doctor

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/agent"
	"github.com/emsnoel/tandem/internal/config"
	"github.com/emsnoel/tandem/internal/herdr"
	"github.com/emsnoel/tandem/internal/pairname"
)

func named(name, pane, kind, cwd string) herdr.Agent {
	return herdr.Agent{PaneID: pane, Name: &name, Kind: kind, CWD: cwd}
}

func env(kv map[string]string) func(string) string {
	return func(k string) string { return kv[k] }
}

var plainNames = pairname.Names{Planner: "planner", Builder: "builder", Reviewer: "reviewer"}

func TestRunningRolesFindsBuilderAndReviewer(t *testing.T) {
	root := t.TempDir()
	p := healthy()
	p.agents = []herdr.Agent{named("planner", "w1:p1", "claude", root),
		named("builder", "w1:p2", "codex", root), named("reviewer", "w1:p3", "claude", root)}
	got := RunningRoles(p, root, plainNames)
	if len(got) != 2 || got[agent.RoleBuilder] != "w1:p2" || got[agent.RoleReviewer] != "w1:p3" {
		t.Fatalf("got %v, want builder w1:p2 and reviewer w1:p3", got)
	}
}

// tandem-pair reads the names Resolve gives it, so a custom name is found.
func TestRunningRolesHonourCustomNames(t *testing.T) {
	root := t.TempDir()
	p := healthy()
	p.agents = []herdr.Agent{named("builder", "w1:p9", "codex", root), named("bob", "w1:p2", "codex", root)}
	got := RunningRoles(p, root, pairname.Names{Planner: "planner", Builder: "bob", Reviewer: "reviewer"})
	if len(got) != 1 || got[agent.RoleBuilder] != "w1:p2" {
		t.Fatalf("got %v, want only builder w1:p2", got)
	}
}

// An agent with the pair's name that works in another checkout is not this
// pair's builder: tandem-pair refuses to adopt it.
func TestRunningRolesIgnoresAnotherCheckout(t *testing.T) {
	root, other := t.TempDir(), t.TempDir()
	p := healthy()
	p.agents = []herdr.Agent{named("builder", "w8:pB5", "codex", other)}
	if got := RunningRoles(p, root, plainNames); len(got) != 0 {
		t.Fatalf("got %v, want none", got)
	}
	rs := PairNames(p, root, plainNames)
	w := find(t, rs, "names:builder")
	want := "pane w8:pB5 holds 'builder' but works in " + other + "; tandem-pair will refuse to adopt it"
	if w.Status != Warn || w.Detail != want {
		t.Fatalf("got %+v, want warning %q", w, want)
	}
}

// An agent working in a subdirectory of the checkout is in it.
func TestRunningRolesAcceptsASubdirectory(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "pkg")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	p := healthy()
	p.agents = []herdr.Agent{named("builder", "w1:p2", "codex", sub)}
	if got := RunningRoles(p, root, plainNames); got[agent.RoleBuilder] != "w1:p2" {
		t.Fatalf("got %v, want builder w1:p2", got)
	}
}

func TestPairNamesReportsTheNames(t *testing.T) {
	rs := PairNames(healthy(), t.TempDir(), pairname.Names{Planner: "planner-wt-b", Builder: "builder-wt-b", Reviewer: "reviewer-wt-b"})
	r := find(t, rs, "names")
	if r.Status != OK || r.Detail != "this checkout pairs as planner-wt-b, builder-wt-b, reviewer-wt-b" {
		t.Fatalf("got %+v", r)
	}
	if len(rs) != 1 {
		t.Fatalf("got %d results, want only names", len(rs))
	}
}

// Review focus 1: the socket check reports the failure; every check still runs.
func TestRunningRolesEmptyWhenHerdrFails(t *testing.T) {
	p := healthy()
	p.socketErr = errors.New("server not running")
	p.agents = []herdr.Agent{named("builder", "w1:p2", "codex", "")}
	if got := RunningRoles(p, t.TempDir(), plainNames); len(got) != 0 {
		t.Fatalf("got %v, want none", got)
	}
}

func runningResults(t *testing.T, manifest string, running map[agent.Role]string) []Result {
	t.Helper()
	root := isolate(t, manifest)
	roles, err := ResolveRoles(root, RoleFlags{}, os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	roles.Running = running
	return Repo(root, root, roles, os.Getenv)
}

func names(rs []Result) string {
	var out []string
	for _, r := range rs {
		out = append(out, r.Name)
	}
	return strings.Join(out, " ")
}

// tandem-pair skips a running builder's prelaunch gates but still resolves its
// launch spec, which checks approval and the launch settings.
func TestRunningBuilderSkipsOnlyItsGates(t *testing.T) {
	rs := runningResults(t, codexRepo, map[agent.Role]string{agent.RoleBuilder: "w1:p2"})
	all := names(rs)
	if strings.Contains(all, "trust:codex:") {
		t.Errorf("got codex gates for a running builder: %s", all)
	}
	for _, want := range []string{"approval:builder", "launch:builder", "trust:claude:"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %s in %s", want, all)
		}
	}
}

// A running reviewer gets no prelaunch gates and no launch spec from tandem-pair.
func TestRunningReviewerSkipsApprovalGatesAndLaunch(t *testing.T) {
	rs := runningResults(t, `{"builder": {"kind": "claude"}, "reviewer": {"kind": "codex"}}`,
		map[agent.Role]string{agent.RoleReviewer: "w1:p3"})
	all := names(rs)
	for _, gone := range []string{"approval:reviewer", "launch:reviewer", "trust:codex:"} {
		if strings.Contains(all, gone) {
			t.Errorf("got %s for a running reviewer: %s", gone, all)
		}
	}
	if r := find(t, rs, "reviewer"); r.Status != OK {
		t.Errorf("reviewer config: got %+v, want ok", r)
	}
}

func TestSharedKindStillCheckedForTheRoleThatStarts(t *testing.T) {
	rs := runningResults(t, `{"builder": {"kind": "codex"}, "reviewer": {"kind": "codex"}}`,
		map[agent.Role]string{agent.RoleReviewer: "w1:p3"})
	if !strings.Contains(names(rs), "trust:codex:project-trust") {
		t.Fatalf("codex gates missing although the builder starts: %s", names(rs))
	}
}

// With the builder running, only the planner uses claude, and a planner's
// manual gate is a warning, as in TestManualGateFailsForBuilderWarnsForPlanner.
func TestRunningBuilderLeavesThePlannerGrade(t *testing.T) {
	root := isolate(t, `{"builder": {"kind": "codex"}, "reviewer": {"kind": "codex"}}`)
	roles, err := ResolveRoles(root, RoleFlags{Builder: config.Overrides{Kind: "claude"}}, os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	roles.Running = map[agent.Role]string{agent.RoleBuilder: "w1:p2"}
	if r := find(t, Repo(root, root, roles, os.Getenv), "trust:claude:project-trust"); r.Status != Warn {
		t.Fatalf("got %+v, want the planner's warning", r)
	}
}

func TestRunningRoleIsReported(t *testing.T) {
	rs := runningResults(t, codexRepo, map[agent.Role]string{
		agent.RoleBuilder: "w1:p2", agent.RoleReviewer: "w1:p3"})
	b := find(t, rs, "running:builder")
	if b.Status != OK || b.Detail != "builder already runs in pane w1:p2, so doctor skips its trust checks, as tandem-pair does" {
		t.Errorf("builder: got %+v", b)
	}
	r := find(t, rs, "running:reviewer")
	if r.Status != OK || r.Detail != "reviewer already runs in pane w1:p3, so doctor skips its approval, trust and launch checks, as tandem-pair does" {
		t.Errorf("reviewer: got %+v", r)
	}
	if !strings.Contains(names(rs), "running:builder running:reviewer") {
		t.Errorf("want builder before reviewer: %s", names(rs))
	}
}

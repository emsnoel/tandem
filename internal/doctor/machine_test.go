package doctor

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/emsnoel/tandem/internal/agent"
	"github.com/emsnoel/tandem/internal/herdr"
)

// fakeProbe answers every probe from fields. A nil map or empty version reads
// as "herdr is not installed" for the call that uses it.
type fakeProbe struct {
	version      string
	versionErr   error
	socketErr    error
	socketBlocks bool
	integrations map[string]herdr.Integration
	intErr       error
	onPath       map[string]string // kind -> "--version" output; "" means unreadable
}

func (f fakeProbe) HerdrVersion(context.Context) (string, error) { return f.version, f.versionErr }

func (f fakeProbe) HerdrSocket(ctx context.Context) error {
	if f.socketBlocks {
		<-ctx.Done()
		return ctx.Err()
	}
	return f.socketErr
}

func (f fakeProbe) Integrations(context.Context) (map[string]herdr.Integration, error) {
	return f.integrations, f.intErr
}

func (f fakeProbe) LookPath(bin string) (string, error) {
	if _, ok := f.onPath[bin]; ok {
		return "/bin/" + bin, nil
	}
	return "", errors.New("not found")
}

func (f fakeProbe) CLIVersion(_ context.Context, bin string) (string, error) {
	if v := f.onPath[bin]; v != "" {
		return v, nil
	}
	return "", errors.New("no version")
}

// healthy is a machine where every check passes for a codex builder.
func healthy() fakeProbe {
	return fakeProbe{
		version:      "0.9.3",
		integrations: map[string]herdr.Integration{"claude": {Kind: "claude", State: "current"}, "codex": {Kind: "codex", State: "current"}},
		onPath:       map[string]string{"claude": "2.1.4", "codex": "codex-cli 0.5.0"},
	}
}

var codexBuilder = Uses{"claude": {agent.RolePlanner}, "codex": {agent.RoleBuilder}}

func inPane(k string) string {
	if k == "HERDR_ENV" {
		return "1"
	}
	return ""
}

func find(t *testing.T, rs []Result, name string) Result {
	t.Helper()
	for _, r := range rs {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("no result %q in %+v", name, rs)
	return Result{}
}

func TestHealthyMachineIsAllOK(t *testing.T) {
	for _, r := range Machine(healthy(), codexBuilder, inPane) {
		if r.Status != OK || r.Group != "machine" {
			t.Errorf("%s: got %s in group %q, want ok in machine", r.Name, r.Status, r.Group)
		}
	}
}

func TestHerdrTooOld(t *testing.T) {
	p := healthy()
	p.version = "0.9.2"
	r := find(t, Machine(p, codexBuilder, inPane), "herdr-version")
	if r.Status != Fail || !strings.Contains(r.Fix, "0.9.3") {
		t.Fatalf("got %+v, want a fail whose fix names 0.9.3", r)
	}
}

// Review focus 1: a machine with no herdr still gets every other check.
func TestHerdrMissingStillChecksCLIs(t *testing.T) {
	gone := &herdr.Error{Code: "not_found", Message: "herdr is not on PATH", Unreachable: true}
	p := healthy()
	p.versionErr, p.socketErr, p.intErr = gone, gone, gone
	rs := Machine(p, codexBuilder, inPane)
	for _, name := range []string{"herdr-version", "herdr-socket", "integrations"} {
		r := find(t, rs, name)
		if r.Status != Fail || r.Fix == "" {
			t.Errorf("%s: got %+v, want a fail with a fix", name, r)
		}
		if strings.Contains(r.Detail, "\t") || !strings.Contains(r.Detail, "herdr is not on PATH") {
			t.Errorf("%s: detail %q should be herdr's message, without its error code", name, r.Detail)
		}
	}
	if r := find(t, rs, "cli:codex"); r.Status != OK {
		t.Errorf("cli:codex: got %+v, want ok", r)
	}
}

// Review focus 2: a herdr that never answers cannot hang doctor.
func TestHerdrHangIsBounded(t *testing.T) {
	p := healthy()
	p.socketBlocks = true
	start := time.Now()
	r := find(t, Machine(p, codexBuilder, inPane), "herdr-socket")
	if r.Status != Fail || !strings.Contains(r.Detail, "did not answer") {
		t.Fatalf("got %+v, want a fail saying herdr did not answer", r)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("took %s, want under 3s", d)
	}
}

func TestOutsideHerdrPaneWarns(t *testing.T) {
	r := find(t, Machine(healthy(), codexBuilder, func(string) string { return "" }), "herdr-pane")
	if r.Status != Warn || r.Fix == "" {
		t.Fatalf("got %+v, want a warn with a fix", r)
	}
}

func TestIntegrationStates(t *testing.T) {
	uses := Uses{"claude": {agent.RolePlanner}, "codex": {agent.RoleBuilder}, "copilot": {agent.RoleReviewer}}
	p := healthy()
	p.onPath["copilot"] = "1.0"
	p.integrations = map[string]herdr.Integration{
		"claude":  {Kind: "claude", State: "outdated", Detail: "v8 < v10"},
		"codex":   {Kind: "codex", State: "current"},
		"copilot": {Kind: "copilot", State: "not installed"},
	}
	rs := Machine(p, uses, inPane)
	if r := find(t, rs, "integration:copilot"); r.Status != Fail || r.Fix != "herdr integration install copilot" {
		t.Errorf("copilot: got %+v", r)
	}
	if r := find(t, rs, "integration:claude"); r.Status != Warn || r.Fix != "herdr integration install claude" ||
		!strings.Contains(r.Detail, "v8 < v10") {
		t.Errorf("claude: got %+v", r)
	}
	if r := find(t, rs, "integration:codex"); r.Status != OK {
		t.Errorf("codex: got %+v", r)
	}
}

func TestMissingCLIFails(t *testing.T) {
	p := healthy()
	delete(p.onPath, "codex")
	r := find(t, Machine(p, codexBuilder, inPane), "cli:codex")
	if r.Status != Fail || !strings.Contains(r.Detail, "builder") || r.Fix == "" {
		t.Fatalf("got %+v, want a fail naming the builder role, with a fix", r)
	}
}

func TestUnreadableCLIVersionWarns(t *testing.T) {
	p := healthy()
	p.onPath["codex"] = ""
	if r := find(t, Machine(p, codexBuilder, inPane), "cli:codex"); r.Status != Warn {
		t.Fatalf("got %+v, want warn", r)
	}
}

// Review focus 5: one kind used by two roles is checked once.
func TestSharedKindListedOnce(t *testing.T) {
	uses := Uses{"claude": {agent.RolePlanner}, "codex": {agent.RoleBuilder, agent.RoleReviewer}}
	n := 0
	for _, r := range Machine(healthy(), uses, inPane) {
		if r.Name == "cli:codex" {
			n++
			if !strings.Contains(r.Detail, "builder, reviewer") {
				t.Errorf("detail %q does not name both roles", r.Detail)
			}
		}
	}
	if n != 1 {
		t.Fatalf("got %d cli:codex results, want 1", n)
	}
}

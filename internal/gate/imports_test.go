package gate

import (
	"os/exec"
	"strings"
	"testing"
)

// The gates must stay pure. go list -deps checks the transitive closure, not
// merely this package's direct imports.
func TestGateReachesNothingImpure(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	forbidden := map[string]bool{
		"os/exec":  true,
		"net":      true,
		"net/http": true,
		"github.com/emsnoel/tandem/internal/herdr":    true,
		"github.com/emsnoel/tandem/internal/vcs":      true,
		"github.com/emsnoel/tandem/internal/decision": true,
		// internal/triage is advisory like internal/decision: a model's opinion must
		// never let a teardown or handoff through.
		"github.com/emsnoel/tandem/internal/triage": true,
	}
	for _, dep := range strings.Fields(string(out)) {
		if forbidden[dep] {
			t.Errorf("internal/gate reaches %q in its dependency closure; the gates must stay pure", dep)
		}
	}
}

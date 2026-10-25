package agent

import (
	"os/exec"
	"strings"
	"testing"
)

// The mapping layer must stay pure so its behaviour is fully determined by its
// inputs. go list -deps checks the transitive closure, not direct imports.
func TestAgentReachesNothingImpure(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	forbidden := map[string]bool{
		"os/exec":  true,
		"net":      true,
		"net/http": true,
		"github.com/emsnoel/tandem/internal/herdr": true,
		"github.com/emsnoel/tandem/internal/vcs":   true,
	}
	for _, dep := range strings.Fields(string(out)) {
		if forbidden[dep] {
			t.Errorf("internal/agent reaches %q; the mapping layer must stay pure", dep)
		}
	}
}

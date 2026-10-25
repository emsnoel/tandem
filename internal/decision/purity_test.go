package decision

import (
	"os/exec"
	"strings"
	"testing"
)

// The decision layer must stay pure. Transport belongs in the jev subpackage,
// which is not part of this package's closure.
func TestDecisionReachesNothingImpure(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	forbidden := map[string]bool{
		"os/exec":  true,
		"os":       true,
		"net":      true,
		"net/http": true,
		"github.com/emsnoel/tandem/internal/gate":         true,
		"github.com/emsnoel/tandem/internal/herdr":        true,
		"github.com/emsnoel/tandem/internal/vcs":          true,
		"github.com/emsnoel/tandem/internal/decision/jev": true,
	}
	for _, dep := range strings.Fields(string(out)) {
		// fmt (used for validation errors) itself depends on os. Check os
		// at project-owned import edges below so that transitive stdlib use
		// does not make this otherwise-pure package fail its own test.
		if forbidden[dep] && dep != "os" {
			t.Errorf("internal/decision reaches %q in its dependency closure; it must stay pure", dep)
		}
	}

	imports, err := exec.Command("go", "list", "-deps", "-f", `{{.ImportPath}} {{join .Imports " "}}`, ".").Output()
	if err != nil {
		t.Fatalf("go list -deps imports: %v", err)
	}
	for _, line := range strings.Split(string(imports), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || !strings.HasPrefix(fields[0], "github.com/emsnoel/tandem/") {
			continue
		}
		for _, imported := range fields[1:] {
			if forbidden[imported] {
				t.Errorf("%q imports %q in the decision dependency closure; it must stay pure", fields[0], imported)
			}
		}
	}
}

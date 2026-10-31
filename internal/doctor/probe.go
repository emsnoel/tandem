package doctor

import (
	"context"
	"os/exec"
	"strings"
	"time"

	"github.com/emsnoel/tandem/internal/herdr"
)

// RealProbe asks the real herdr and the real agent CLIs.
type RealProbe struct{}

func (RealProbe) HerdrVersion(ctx context.Context) (string, error) { return herdr.Version(ctx) }

// Agents lists agents, which only succeeds when the server answers on its
// socket.
func (RealProbe) Agents(ctx context.Context) ([]herdr.Agent, error) {
	d := probeTimeout
	if dl, ok := ctx.Deadline(); ok {
		d = time.Until(dl)
	}
	return herdr.ListWithin(d)
}

func (RealProbe) Integrations(ctx context.Context) (map[string]herdr.Integration, error) {
	return herdr.IntegrationStatus(ctx)
}

func (RealProbe) LookPath(bin string) (string, error) { return exec.LookPath(bin) }

// CLIVersion returns the first line of `bin --version`.
func (RealProbe) CLIVersion(ctx context.Context, bin string) (string, error) {
	cmd := exec.CommandContext(ctx, bin, "--version")
	// A killed CLI can leave a child holding its output open; stop waiting for
	// it a second after the kill, as internal/herdr does.
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	first, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	return first, nil
}

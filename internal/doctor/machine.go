package doctor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/emsnoel/tandem/internal/herdr"
)

// Probe is everything the machine checks ask of the outside world.
type Probe interface {
	HerdrVersion(ctx context.Context) (string, error)
	HerdrSocket(ctx context.Context) error
	Integrations(ctx context.Context) (map[string]herdr.Integration, error)
	LookPath(bin string) (string, error)
	CLIVersion(ctx context.Context, bin string) (string, error)
}

// socketFix matches the exit-3 remedy in guide/refusals.md.
const socketFix = "check the Herdr server is running, then run tandem doctor again from a pane Herdr manages"

// Machine checks Herdr and the agent CLIs for the kinds in uses.
func Machine(p Probe, uses Uses, getenv func(string) string) []Result {
	rs := []Result{herdrVersion(p), herdrSocket(p), herdrPane(getenv)}
	rs = append(rs, integrations(p, uses)...)
	for _, kind := range uses.kinds() {
		rs = append(rs, cli(p, kind, uses.roles(kind)))
	}
	for i := range rs {
		rs[i].Group = "machine"
	}
	return rs
}

// errText is herdr's own message without its error code, or err's text.
func errText(err error) string {
	var he *herdr.Error
	if errors.As(err, &he) {
		return he.Message
	}
	return err.Error()
}

func bounded[T any](f func(context.Context) (T, error)) (T, error) {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	v, err := f(ctx)
	if err != nil && ctx.Err() != nil {
		return v, fmt.Errorf("did not answer within %s", probeTimeout)
	}
	return v, err
}

func herdrVersion(p Probe) Result {
	r := Result{Name: "herdr-version"}
	v, err := bounded(p.HerdrVersion)
	fix := "install Herdr " + herdr.MinVersion + " or later"
	switch {
	case err != nil:
		r.Status, r.Detail, r.Fix = Fail, "herdr: "+errText(err), fix
	case !herdr.VersionAtLeast(v, herdr.MinVersion):
		r.Status, r.Detail, r.Fix = Fail, "herdr "+v+" is older than "+herdr.MinVersion, fix
	default:
		r.Status, r.Detail = OK, "herdr "+v+" (needs "+herdr.MinVersion+"+)"
	}
	return r
}

func herdrSocket(p Probe) Result {
	_, err := bounded(func(ctx context.Context) (struct{}, error) { return struct{}{}, p.HerdrSocket(ctx) })
	if err != nil {
		return Result{Name: "herdr-socket", Status: Fail, Detail: "herdr socket: " + errText(err), Fix: socketFix}
	}
	return Result{Name: "herdr-socket", Status: OK, Detail: "herdr socket reachable"}
}

func herdrPane(getenv func(string) string) Result {
	if getenv("HERDR_ENV") == "1" {
		return Result{Name: "herdr-pane", Status: OK, Detail: "running inside a Herdr pane"}
	}
	return Result{Name: "herdr-pane", Status: Warn,
		Detail: "not inside a Herdr pane, and tandem-pair must run from one",
		Fix:    "run tandem-pair from a pane Herdr manages"}
}

func integrations(p Probe, uses Uses) []Result {
	got, err := bounded(p.Integrations)
	if err != nil {
		return []Result{{Name: "integrations", Status: Fail,
			Detail: "could not read herdr integration status: " + errText(err),
			Fix:    "install Herdr " + herdr.MinVersion + " or later, then run tandem doctor again"}}
	}
	var rs []Result
	for _, kind := range uses.kinds() {
		r := Result{Name: "integration:" + kind, Fix: "herdr integration install " + kind}
		in, ok := got[kind]
		switch {
		case !ok || in.State == "not installed":
			r.Status, r.Detail = Fail, "herdr integration for "+kind+" not installed"
		case in.State == "outdated":
			r.Status, r.Detail = Warn, "herdr integration for "+kind+" outdated ("+in.Detail+")"
		default:
			r.Status, r.Detail, r.Fix = OK, "herdr integration for "+kind+" "+in.State, ""
		}
		rs = append(rs, r)
	}
	return rs
}

// cli checks one agent CLI. Herdr starts the binary named by the kind, so the
// kind is the binary name.
func cli(p Probe, kind, roles string) Result {
	r := Result{Name: "cli:" + kind}
	if _, err := p.LookPath(kind); err != nil {
		r.Status, r.Detail = Fail, kind+" not on PATH ("+roles+")"
		r.Fix = "install the " + kind + " CLI, then run tandem doctor again"
		return r
	}
	v, err := bounded(func(ctx context.Context) (string, error) { return p.CLIVersion(ctx, kind) })
	if err != nil || v == "" {
		if err == nil {
			err = errors.New("empty output")
		}
		r.Status, r.Detail = Warn, kind+" on PATH, version unreadable: "+err.Error()+" ("+roles+")"
		r.Fix = "check that `" + kind + " --version` runs"
		return r
	}
	// Some CLIs start with the number; name the kind first so the line stands
	// alone, unless the text before the number already names it.
	v = strings.TrimSuffix(v, ".")
	lead := v[:strings.IndexFunc(v+"0", unicode.IsDigit)]
	if !strings.Contains(strings.ToLower(lead), kind) {
		v = kind + " " + v
	}
	r.Status, r.Detail = OK, v+" on PATH ("+roles+")"
	return r
}

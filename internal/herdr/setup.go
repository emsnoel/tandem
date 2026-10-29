package herdr

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// MinVersion is the oldest Herdr tandem supports. Every wait reads Herdr's
// event stream, whose versioned schema (protocol 22) arrived in 0.9.3.
const MinVersion = "0.9.3"

// Integration is one line of `herdr integration status`. State is "current",
// "outdated" or "not installed". Detail is the parenthesised note before the
// path, such as "v8 < v10", or "" when there is none.
type Integration struct {
	Kind   string
	State  string
	Detail string
}

// ParseVersion reads the version number from `herdr --version` output.
func ParseVersion(out string) (string, error) {
	for _, f := range strings.Fields(out) {
		if f != "" && f[0] >= '0' && f[0] <= '9' && strings.Contains(f, ".") {
			return f, nil
		}
	}
	return "", fmt.Errorf("no version in herdr --version output %q", strings.TrimSpace(out))
}

// VersionAtLeast compares dotted version numbers part by part, numerically. A
// missing part counts as 0, so "1.0" is at least "0.9.3".
func VersionAtLeast(have, min string) bool {
	h, m := strings.Split(have, "."), strings.Split(min, ".")
	for i := 0; i < len(h) || i < len(m); i++ {
		a, b := part(h, i), part(m, i)
		if a != b {
			return a > b
		}
	}
	return true
}

func part(parts []string, i int) int {
	if i >= len(parts) {
		return 0
	}
	n, _ := strconv.Atoi(parts[i])
	return n
}

// ParseIntegrationStatus reads `herdr integration status`, one integration per
// line: `kind[ (experimental)]: state[ (detail)] (path)`. It is keyed by kind.
func ParseIntegrationStatus(out string) map[string]Integration {
	got := map[string]Integration{}
	for _, line := range strings.Split(out, "\n") {
		name, rest, ok := strings.Cut(line, ": ")
		if !ok {
			continue
		}
		kind := strings.Fields(name)[0]
		// The last parenthesised group is the integration's path.
		if i := strings.LastIndex(rest, " ("); i >= 0 {
			rest = rest[:i]
		}
		state, detail := rest, ""
		if i := strings.Index(rest, " ("); i >= 0 {
			state, detail = rest[:i], strings.TrimSuffix(rest[i+2:], ")")
		}
		got[kind] = Integration{Kind: kind, State: state, Detail: detail}
	}
	return got
}

// Version runs `herdr --version`, bounded by ctx.
func Version(ctx context.Context) (string, error) {
	b, err := rawContext(ctx, "--version")
	if err != nil {
		return "", err
	}
	return ParseVersion(string(b))
}

// IntegrationStatus runs `herdr integration status`, bounded by ctx.
func IntegrationStatus(ctx context.Context) (map[string]Integration, error) {
	b, err := rawContext(ctx, "integration", "status")
	if err != nil {
		return nil, err
	}
	return ParseIntegrationStatus(string(b)), nil
}

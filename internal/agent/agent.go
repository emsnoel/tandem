// Package agent maps tandem's normalised launch vocabulary onto the native
// flags of each supported agent CLI.
//
// The package is pure: Resolve is a total function from Spec to Launch and
// touches no process, socket or file. Code that writes another tool's config
// lives elsewhere, and a dependency-closure test enforces it.
//
// Direction means permissiveness only: serving `readonly` as `ask` is a
// widening, even though it also stalls an unattended pane.
package agent

import (
	"fmt"
	"sort"
	"strings"
)

type Role string

const (
	RolePlanner  Role = "planner"
	RoleBuilder  Role = "builder"
	RoleReviewer Role = "reviewer"
)

// Unattended reports whether the role runs in a pane with no human in front of
// it. Such a pane must never wait on a question only a person could answer.
func (r Role) Unattended() bool { return r == RoleBuilder || r == RoleReviewer }

type Autonomy string

const (
	Readonly Autonomy = "readonly"
	Ask      Autonomy = "ask"
	Auto     Autonomy = "auto"
	Full     Autonomy = "full"
)

// AutonomyOrder is the canonical permissiveness order. Index position is the
// only thing that decides whether a substitution widens or narrows.
var AutonomyOrder = []string{"readonly", "ask", "auto", "full"}

type Effort string

const (
	EffortLow    Effort = "low"
	EffortMedium Effort = "medium"
	EffortHigh   Effort = "high"
	EffortMax    Effort = "max"
)

var EffortOrder = []string{"low", "medium", "high", "max"}

type Direction int

const (
	Narrowing Direction = iota
	Widening
)

func (d Direction) String() string {
	if d == Widening {
		return "widening"
	}
	return "narrowing"
}

// Warning records a substitution that was permitted. Substituted is empty when
// the axis was dropped entirely.
type Warning struct {
	Axis        string    `json:"axis"`
	Requested   string    `json:"requested"`
	Substituted string    `json:"substituted"`
	Direction   Direction `json:"-"`
	DirectionS  string    `json:"direction"`
	Detail      string    `json:"detail"`
}

// Error is a refusal. Remedy is the exact edit or flag that resolves it, so
// callers print it verbatim rather than re-authoring the wording.
type Error struct {
	Code      string    `json:"code"`
	Axis      string    `json:"axis"`
	Value     string    `json:"value"`
	Nearest   string    `json:"nearest,omitempty"`
	Direction Direction `json:"-"`
	// DirectionS is the wire form of Direction. Both exist because callers
	// switch on the typed value while the JSON consumer reads the string.
	DirectionS string   `json:"direction,omitempty"`
	Adapter    string   `json:"adapter"`
	Supported  []string `json:"supported,omitempty"`
	Exit       int      `json:"exit"`
	Remedy     string   `json:"remedy"`
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s: %s=%q is not supported by %s", e.Code, e.Axis, e.Value, e.Adapter)
}

// Spec is what the caller asked for, normalised.
type Spec struct {
	Kind     string
	Role     Role
	Model    string
	Effort   Effort
	Autonomy Autonomy
	Cwd      string
	// SessionID, when set, names a new session for adapters that can start one.
	// The caller generates it so Resolve stays deterministic.
	SessionID    string
	ExtraArgs    []string
	AllowDegrade []string
	AllowWiden   []string
}

// Launch is what to actually run.
type Launch struct {
	Kind      string    `json:"kind"`
	Args      []string  `json:"args"`
	Warnings  []Warning `json:"warnings"`
	Sandboxed bool      `json:"sandboxed"`
}

type Adapter interface {
	Kind() string
	// Supports returns the normalised values this adapter honours for an axis,
	// in increasing permissiveness. An empty slice means the axis cannot be
	// expressed at all.
	Supports(axis string) []string
	// Persona returns the native persona name for a role, or "" when the CLI
	// has no persona that can be selected without user-defined configuration.
	Persona(Role) string
	Resolve(Spec) (Launch, error)
	// Sandboxed predicts whether this configuration blocks the Herdr socket.
	// It is informational and must not gate behaviour.
	Sandboxed(Spec) bool
	// ResetCommand is the text that, typed into this CLI's composer and
	// submitted, starts a new conversation in the same process and pane. ""
	// means there is no MEASURED way to; callers then leave the conversation
	// alone. internal/agent/reset_test.go pins each value to its evidence.
	ResetCommand() string
	// ResetConfirmKey is the Herdr key pressed once when ResetCommand changed
	// nothing Herdr can see, because the CLI opened a dialog first. "" means none.
	// Pinned in internal/agent/reset_test.go.
	ResetConfirmKey() string
	// InstructionFiles names the repo-root files this CLI loads into every new
	// conversation, so the launch prompt does not ask for them again. nil means
	// unmeasured, and the prompt keeps asking. Pinned in
	// internal/agent/reset_test.go.
	InstructionFiles() []string
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

func indexOf(hay []string, needle string) int {
	for i, h := range hay {
		if h == needle {
			return i
		}
	}
	return -1
}

// Degrade resolves one axis against an adapter's supported subset. canonical is
// the full vocabulary in increasing permissiveness. It prefers the nearest
// supported value below the request, since withholding authority is safer, and
// goes above only when nothing below exists. A refused narrowing is an error,
// never a silent widening.
func Degrade(axis, requested string, canonical, supported, allowDegrade, allowWiden []string, kind string) (string, *Warning, error) {
	if requested == "" {
		return "", nil, nil // unset; the adapter's own default applies
	}
	if contains(supported, requested) {
		return requested, nil, nil
	}

	if len(supported) == 0 {
		if !contains(allowDegrade, axis) {
			return "", nil, &Error{
				Code: axis + "_unsupported", Axis: axis, Value: requested,
				Adapter: kind, Exit: 20, Direction: Narrowing, DirectionS: "narrowing",
				Remedy: fmt.Sprintf("%s cannot express %s on the interactive path; add %q to allow_degrade in the repo manifest (tandem paths manifest) to drop the axis, or leave %s unset",
					kind, axis, axis, axis),
			}
		}
		return "", &Warning{
			Axis: axis, Requested: requested, Direction: Narrowing, DirectionS: "narrowing",
			Detail: fmt.Sprintf("%s cannot express %s; the axis was dropped", kind, axis),
		}, nil
	}

	i := indexOf(canonical, requested)
	if i < 0 {
		return "", nil, &Error{
			Code: axis + "_unknown", Axis: axis, Value: requested, Adapter: kind,
			Supported: supported, Exit: 20,
			Remedy: fmt.Sprintf("%q is not a %s value; use one of: %s", requested, axis, strings.Join(canonical, ", ")),
		}
	}

	for j := i - 1; j >= 0; j-- {
		if !contains(supported, canonical[j]) {
			continue
		}
		if !contains(allowDegrade, axis) {
			return "", nil, &Error{
				Code: axis + "_narrowing_refused", Axis: axis, Value: requested,
				Nearest: canonical[j], Direction: Narrowing, DirectionS: "narrowing", Adapter: kind,
				Supported: supported, Exit: 20,
				Remedy: fmt.Sprintf("%s does not support %s=%s; the nearest is %q, which withholds authority you asked for. Add %q to allow_degrade in the repo manifest (tandem paths manifest), or set %s to one of: %s",
					kind, axis, requested, canonical[j], axis, axis, strings.Join(supported, ", ")),
			}
		}
		return canonical[j], &Warning{
			Axis: axis, Requested: requested, Substituted: canonical[j],
			Direction: Narrowing, DirectionS: "narrowing",
			Detail: fmt.Sprintf("%s does not support %s=%s", kind, axis, requested),
		}, nil
	}

	for j := i + 1; j < len(canonical); j++ {
		if !contains(supported, canonical[j]) {
			continue
		}
		if !contains(allowWiden, axis) {
			return "", nil, &Error{
				Code: axis + "_widening_refused", Axis: axis, Value: requested,
				Nearest: canonical[j], Direction: Widening, DirectionS: "widening", Adapter: kind,
				Supported: supported, Exit: 21,
				Remedy: fmt.Sprintf("%s has no %s below %q; the nearest is %q, which GRANTS authority you did not request. Add \"allow_widen\": [%q] under adapters.%s in ~/.config/herdr/tandem.json, or set %s to one of: %s",
					kind, axis, requested, canonical[j], axis, kind, axis, strings.Join(supported, ", ")),
			}
		}
		return canonical[j], &Warning{
			Axis: axis, Requested: requested, Substituted: canonical[j],
			Direction: Widening, DirectionS: "widening",
			Detail: fmt.Sprintf("%s has no %s below %s", kind, axis, requested),
		}, nil
	}

	return "", nil, &Error{
		Code: axis + "_unsupported", Axis: axis, Value: requested, Adapter: kind,
		Supported: supported, Exit: 20,
		Remedy: fmt.Sprintf("%s supports no %s value comparable to %q", kind, axis, requested),
	}
}

var registry = map[string]Adapter{}

// Register adds an adapter. Adapters call this from their own init.
func Register(a Adapter) { registry[a.Kind()] = a }

func Get(kind string) (Adapter, error) {
	a, ok := registry[kind]
	if !ok {
		return nil, &Error{
			Code: "unknown_kind", Axis: "kind", Value: kind, Exit: 20,
			Supported: Kinds(),
			Remedy:    "supported kinds: " + strings.Join(Kinds(), ", "),
		}
	}
	return a, nil
}

func Kinds() []string {
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

package agent

// Disposition says whether tandem may act on a gate or only observe it.
// ~/.codex/config.toml is edited by humans and read by Codex, so appending is
// safe. ~/.claude.json is rewritten by every live Claude Code session, so a
// read-modify-write would race them.
type Disposition int

const (
	// Apply: tandem may write, and what it writes carries a marker so it
	// can be identified and removed later.
	Apply Disposition = iota
	// Report: detect and tell the human. Never write.
	Report
)

func (d Disposition) String() string {
	if d == Report {
		return "report"
	}
	return "apply"
}

type Status int

const (
	StatusCurrent Status = iota // already satisfied; nothing to do
	StatusApplied               // we just satisfied it
	StatusManual                // a human must act; Remedy says how
	StatusAbsent                // the tool this gate belongs to is not installed
	StatusFailed                // we tried and could not
	// StatusWarning: a human should act, but the launch is sound without it. A
	// builder refusal is reserved for gates that would stall the pane; a finding
	// that only degrades the agent is reported loudly without stopping the pairing.
	StatusWarning
)

func (s Status) String() string {
	switch s {
	case StatusApplied:
		return "applied"
	case StatusManual:
		return "manual"
	case StatusAbsent:
		return "absent"
	case StatusFailed:
		return "failed"
	case StatusWarning:
		return "warning"
	default:
		return "current"
	}
}

// Action is one gate's finding. Remedy is the exact command or edit that
// resolves a Manual status, authored once here so every caller prints the same
// words.
type Action struct {
	Gate         string      `json:"gate"`
	Disposition  Disposition `json:"-"`
	DispositionS string      `json:"disposition"`
	Status       Status      `json:"-"`
	StatusS      string      `json:"status"`
	Target       string      `json:"target"`
	Detail       string      `json:"detail"`
	Remedy       string      `json:"remedy,omitempty"`
}

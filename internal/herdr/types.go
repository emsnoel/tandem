package herdr

// Agent is one entry from `herdr agent list` or `herdr agent get`. Name is a
// pointer because herdr emits null for an unnamed pane. Status is required: a
// pointer only so its absence can be detected, which means the schema moved
// and must fail loudly.
type Agent struct {
	PaneID string  `json:"pane_id"`
	Name   *string `json:"name"`
	// Kind is the agent Herdr detected in the pane: claude, codex and so on.
	Kind   string  `json:"agent"`
	CWD    string  `json:"cwd"`
	Status *string `json:"agent_status"`
	// Session is what the agent's Herdr integration last reported as its
	// conversation. Optional: absent until an integration reports one.
	Session *AgentSession `json:"agent_session"`
	// StateChangeSeq counts every lifecycle change Herdr has seen for the
	// pane, however brief, so a change between two polls still shows. 0 when
	// absent.
	StateChangeSeq int `json:"state_change_seq"`
}

// listEnvelope, getEnvelope and errEnvelope describe herdr's JSON responses.
// Result is a pointer so a missing "result" decodes to nil.
type listEnvelope struct {
	Result *struct {
		Agents []Agent `json:"agents"`
	} `json:"result"`
}

type getEnvelope struct {
	Result *struct {
		Agent *Agent `json:"agent"`
	} `json:"result"`
}

type errEnvelope struct {
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// AgentSession is one agent's reported conversation identity.
type AgentSession struct {
	Value string `json:"value"`
}

// SessionID returns the reported session, or "" when none was reported.
func (a *Agent) SessionID() string {
	if a == nil || a.Session == nil {
		return ""
	}
	return a.Session.Value
}

// What Herdr's agent_session means for each kind:
//
//   - claude: the pane's own conversation; the hook runs in the pane's process.
//   - copilot: absent until the first turn, then always the launch
//     --session-id, kept after /new.
//   - codex: the hook runs in Codex's shared app-server daemon, started by the
//     first Codex TUI on the machine. Every Codex conversation is credited to
//     the pane that started the daemon, and no other pane gets one.
//   - opencode: never reported.

// Conversation returns the conversation the agent is in, when Herdr's report
// names it: only for Claude Code. For every other kind it is "", and the
// conversation is found from the CLI's own records instead.
func (a *Agent) Conversation() string {
	if a == nil || !NamesConversation(a.Kind) {
		return ""
	}
	return a.SessionID()
}

// NamesConversation reports whether Herdr's session for a pane of kind is
// that pane's own conversation: only for Claude Code (see above).
func NamesConversation(kind string) bool { return kind == "claude" }

// HasNoConversation reports whether the pane is known to have no conversation:
// no session reported, for a kind where that means no turn yet (Claude and
// Copilot). For Codex and opencode it is false.
func (a *Agent) HasNoConversation() bool {
	return a != nil && (a.Kind == "claude" || a.Kind == "copilot") && a.SessionID() == ""
}

// StatusString returns agent_status, or "" when it is missing.
func (a *Agent) StatusString() string {
	if a == nil || a.Status == nil {
		return ""
	}
	return *a.Status
}

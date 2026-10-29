package trust

func init() {
	RegisterNoGates("opencode",
		"opencode's interactive TUI has no trust dialog, per-command approval "+
			"prompt, or hook-acceptance dialog Herdr could walk into unattended -- "+
			"see Sandboxed() in internal/agent/opencode.go and the per-kind "+
			"launch-gate table in guide/launching.md")
}

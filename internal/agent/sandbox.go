package agent

import "slices"

// SandboxGrant is what a user lets a sandboxed builder or reviewer do beyond
// writing its own checkout. A build tool's check can need both: Gradle locks
// files under ~/.gradle and talks to its own processes over loopback sockets.
//
// Only the user config may set it, since it grants authority. An adapter whose
// agent runs without tandem's sandbox has nothing to widen and ignores it.
type SandboxGrant struct {
	// WritableDirs are absolute directories the agent may write.
	WritableDirs []string `json:"writable_dirs,omitempty"`
	// Network lets the agent's commands open network sockets, loopback
	// included. The sandbox denies both by default.
	Network bool `json:"network,omitempty"`
}

// Covers reports whether g grants everything need asks for.
func (g SandboxGrant) Covers(need SandboxGrant) bool {
	if need.Network && !g.Network {
		return false
	}
	for _, d := range need.WritableDirs {
		if !slices.Contains(g.WritableDirs, d) {
			return false
		}
	}
	return true
}

package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/emsnoel/tandem/internal/agent"
	"github.com/emsnoel/tandem/internal/config"
)

// buildTool is a build tool whose check cannot run in a default sandbox, and
// what the sandbox must grant for it to run.
type buildTool struct {
	name string
	// markers are root files that show the repository builds with the tool.
	markers []string
	// word, found in the check command, shows the check runs the tool.
	word string
	// need is the grant the tool's check needs, given the user's home and
	// the environment, which may move the tool's own home.
	need func(home string, getenv func(string) string) agent.SandboxGrant
}

// buildTools lists only tools measured failing in codex-cli 0.169.0's
// workspace-write sandbox. Gradle's wrapper locks a file under ~/.gradle, and
// Gradle binds a loopback socket for its file lock contention handler, so
// `./gradlew help --offline` fails on either without both grants.
var buildTools = []buildTool{{
	name:    "gradle",
	markers: []string{"gradlew", "settings.gradle", "settings.gradle.kts", "build.gradle", "build.gradle.kts"},
	word:    "gradle",
	need: func(home string, getenv func(string) string) agent.SandboxGrant {
		g := agent.SandboxGrant{Network: true}
		dir := getenv("GRADLE_USER_HOME")
		if dir == "" && home != "" {
			dir = filepath.Join(home, ".gradle")
		}
		if dir != "" {
			g.WritableDirs = []string{filepath.Clean(dir)}
		}
		return g
	},
}}

// sandboxNeeds lists, in buildTools order, the tools root's check uses.
func sandboxNeeds(root, check string) []buildTool {
	var out []buildTool
	for _, t := range buildTools {
		uses := strings.Contains(check, t.word)
		for _, m := range t.markers {
			if _, err := os.Stat(filepath.Join(root, m)); err == nil {
				uses = true
			}
		}
		if uses {
			out = append(out, t)
		}
	}
	return out
}

// sandbox warns when a sandboxed builder or reviewer could not run the
// project's check, because its sandbox lacks what the check's build tool
// needs. It reads files and config only, and never runs the check.
func sandbox(root, cwd string, res config.Resolved, check string, getenv func(string) string) []Result {
	tools := sandboxNeeds(root, check)
	if !res.Role.Unattended() || len(tools) == 0 {
		return nil
	}
	a, err := agent.Get(res.Kind.Value)
	if err != nil {
		return nil
	}
	l, err := a.Resolve(res.Spec(cwd))
	if err != nil || !l.Sandboxed {
		return nil // launch reports a refusal, and no sandbox needs no grant
	}
	var rs []Result
	for _, t := range tools {
		rs = append(rs, sandboxResult(res, t.name, t.need(getenv("HOME"), getenv), l.Granted))
	}
	return rs
}

func sandboxResult(res config.Resolved, tool string, need, granted agent.SandboxGrant) Result {
	who := res.Kind.Value + " " + string(res.Role)
	r := Result{Name: "sandbox:" + string(res.Role)}
	if !granted.Covers(need) {
		r.Status = Warn
		r.Detail = fmt.Sprintf("the %s's sandbox blocks %s, so it cannot run the check", who, tool)
		grant := fmt.Sprintf(`add "sandbox": {"writable_dirs": ["%s"], "network": true} under adapters.%s in %s, then restart the %s`,
			strings.Join(need.WritableDirs, `", "`), res.Kind.Value, config.UserConfigPath(), res.Role)
		r.Fix = grant
		if res.Autonomy.Value == string(agent.Readonly) {
			r.Detail = fmt.Sprintf("the %s runs read-only, so it cannot run the %s check", who, tool)
			r.Fix = "set " + string(res.Role) + ".autonomy to auto in .tandem.json, and " + grant
		}
		return r
	}
	for _, d := range need.WritableDirs {
		if st, err := os.Stat(d); err != nil || !st.IsDir() {
			r.Status = Warn
			r.Detail = fmt.Sprintf("the %s may write %s, but it does not exist and the sandbox cannot create it", who, d)
			r.Fix = "run the check once outside the sandbox, or mkdir " + d
			return r
		}
	}
	r.Status, r.Detail = OK, fmt.Sprintf("the %s's sandbox lets %s run the check", who, tool)
	return r
}

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/emsnoel/tandem/internal/exitcode"
)

// cmdBuildRoles answers "who is in this build" from the legacy build record:
// the plan, builder, reviewer and planner, as NUL fields. It reads the four
// keys leniently, not through build.Load, so tandem-bridge still gets an
// answer from a record build.Load would refuse. A missing builder is
// "builder", as the bridge has always assumed.
func cmdBuildRoles(args []string) int {
	fs := flag.NewFlagSet("build roles", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	relay := fs.String("relay", "", "the relay mailbox holding build/build.json")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *relay == "" {
		fmt.Fprintln(os.Stderr, "usage: tandem build roles --relay DIR")
		return exitcode.Usage
	}
	path := filepath.Join(*relay, "build", "build.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return fail(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil || raw == nil {
		return fail(fmt.Errorf("%s is not a JSON object", path))
	}
	str := func(k string) string {
		var s string
		_ = json.Unmarshal(raw[k], &s)
		return s
	}
	builder := str("builder")
	if builder == "" {
		builder = "builder"
	}
	writeFields(os.Stdout, str("plan"), builder, str("reviewer"), str("planner"))
	return exitcode.OK
}

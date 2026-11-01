package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/emsnoel/tandem/internal/exitcode"
)

// relayReadKeys are the fields `tandem relay read` prints, in order, each with
// the value a missing key takes. from has no default: the bridge's question
// path and task path default an empty sender differently.
var relayReadKeys = []struct{ key, def string }{
	{"id", ""}, {"kind", "question"}, {"from", ""}, {"question", ""}, {"message", ""},
	{"created", ""}, {"task", ""}, {"report_path", ""}, {"stuck", "false"}, {"review", "false"},
	{"verdict", ""},
}

// cmdRelayRead prints a mailbox message as NUL fields for tandem-bridge. It
// reads the way the bridge's jq did: a string prints as it is, any other value
// as its JSON text, and null, false or a missing key takes the default. A file
// that is not a JSON object prints nothing.
func cmdRelayRead(args []string) int {
	fs := flag.NewFlagSet("relay read", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	file := fs.String("file", "", "the queued message")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *file == "" {
		fmt.Fprintln(os.Stderr, relayUse)
		return exitcode.Usage
	}
	b, err := os.ReadFile(*file)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tandem relay:", err)
		return exitcode.Internal
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil || raw == nil {
		fmt.Fprintf(os.Stderr, "tandem relay: %s is not a JSON object\n", *file)
		return exitcode.Internal
	}
	fields := make([]string, len(relayReadKeys))
	for i, k := range relayReadKeys {
		fields[i] = relayValue(raw[k.key], k.def)
	}
	writeFields(os.Stdout, fields...)
	return exitcode.OK
}

// relayValue is jq's `(.k // def) | tostring` for one raw value.
func relayValue(v json.RawMessage, def string) string {
	v = bytes.TrimSpace(v)
	switch string(v) {
	case "", "null", "false":
		return def
	}
	var s string
	if json.Unmarshal(v, &s) == nil {
		return s
	}
	var c bytes.Buffer
	if json.Compact(&c, v) != nil {
		return def
	}
	return c.String()
}

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
)

// jsonOut, when set, is where printJSON writes in place of os.Stdout. build
// start --fields points it at a buffer for the length of one start.
var jsonOut io.Writer

// startFieldKeys is the order of build start --fields, the keys
// bin/tandem-handoff reads from either engine's answer.
var startFieldKeys = []string{"engine", "run_id", "dry_run", "task", "total", "title", "brief", "ledger",
	"resumed", "replaced", "judge", "reviewer", "review_pending", "review_prompt"}

// cmdBuildStartFields runs build start with args, then prints the last JSON
// object it wrote as NUL fields in startFieldKeys order, whatever the exit
// code. A missing key, null and false print empty, as jq's `// empty` did.
func cmdBuildStartFields(args []string) int {
	var buf bytes.Buffer
	old := jsonOut
	jsonOut = &buf
	rc := cmdBuild(append([]string{"start"}, args...))
	jsonOut = old

	var last map[string]any
	dec := json.NewDecoder(&buf)
	dec.UseNumber()
	for {
		var m map[string]any
		if dec.Decode(&m) != nil {
			break
		}
		last = m
	}
	vals := make([]string, len(startFieldKeys))
	for i, k := range startFieldKeys {
		switch v := last[k].(type) {
		case string:
			vals[i] = v
		case json.Number:
			vals[i] = v.String()
		case bool:
			if v {
				vals[i] = "true"
			}
		}
	}
	writeFields(os.Stdout, vals...)
	return rc
}

// withoutFlag returns args with every bare flag removed, and whether it was there.
func withoutFlag(args []string, flag string) ([]string, bool) {
	out, found := make([]string, 0, len(args)), false
	for _, a := range args {
		if a == flag {
			found = true
			continue
		}
		out = append(out, a)
	}
	return out, found
}

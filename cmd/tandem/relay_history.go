package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/emsnoel/tandem/internal/exitcode"
)

// relayHistoryKeys are the Q&A record's fields, in the order
// `tandem relay history` prints them after the file and its JSON.
var relayHistoryKeys = []string{"id", "ts", "from", "to", "question", "answer"}

// cmdRelayHistory prints the Q&A log for bin/tandem-relaylog, oldest first.
// Ids start with the date and time, so sorting the names sorts the records.
// Review triage writes judge-<id>.json beside them; those are left out. Each
// record is 8 NUL fields: the file, its JSON on one line, then id, ts, from,
// to, question and answer, read as `relay read` reads a value. A file that is
// not a JSON object prints its path and seven empty fields, so the script can
// say which file it skipped.
func cmdRelayHistory(args []string) int {
	fs := flag.NewFlagSet("relay history", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dir := fs.String("dir", "", "the Q&A log directory")
	limit := fs.String("limit", "0", "print only the last N records; 0 prints all")
	var n int
	err := fs.Parse(args)
	if err == nil {
		n, err = strconv.Atoi(*limit)
	}
	if err != nil || n < 0 || fs.NArg() != 0 || *dir == "" {
		fmt.Fprintln(os.Stderr, relayUse)
		return exitcode.Usage
	}
	// ReadDir, not Glob, so a directory path holding [ * or ? is read as a
	// path. Dot files stay out, as bash's "$LOG_DIR"/*.json left them out.
	entries, _ := os.ReadDir(*dir)
	var names []string
	for _, e := range entries {
		n := e.Name()
		if !e.IsDir() && strings.HasSuffix(n, ".json") && !strings.HasPrefix(n, "judge-") && !strings.HasPrefix(n, ".") {
			names = append(names, filepath.Join(*dir, n))
		}
	}
	sort.Strings(names)
	if n > 0 && len(names) > n {
		names = names[len(names)-n:]
	}
	for _, f := range names {
		writeFields(os.Stdout, historyRecord(f)...)
	}
	return exitcode.OK
}

// historyRecord is one file's 8 fields.
func historyRecord(file string) []string {
	out := make([]string, 2+len(relayHistoryKeys))
	out[0] = file
	b, err := os.ReadFile(file)
	if err != nil {
		return out
	}
	var raw map[string]json.RawMessage
	var c bytes.Buffer
	if json.Unmarshal(b, &raw) != nil || raw == nil || json.Compact(&c, b) != nil {
		return out
	}
	out[1] = c.String()
	for i, k := range relayHistoryKeys {
		out[2+i] = relayValue(raw[k], "")
	}
	return out
}

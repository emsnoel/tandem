package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/rollout"
)

// cmdUsage prints where a Codex session's tokens went, one row per phase.
// Pass the review session's rollout and the build session's rollout together:
// since the build starts in a fresh conversation, it lives in its own file.
func cmdUsage(args []string) int {
	fs := flag.NewFlagSet("usage", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "print {phases, total} as JSON")
	if err := fs.Parse(args); err != nil {
		return exitcode.Usage
	}
	if fs.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: tandem usage [--json] ROLLOUT.jsonl... (Codex rollout logs only)")
		return exitcode.Usage
	}
	var all []rollout.Phase
	for _, path := range fs.Args() {
		f, err := os.Open(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return exitcode.Internal
		}
		ps, err := rollout.Parse(f, filepath.Base(path))
		f.Close()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return exitcode.Internal
		}
		all = append(all, ps...)
	}
	total := rollout.Total(all)
	if *asJSON {
		_ = json.NewEncoder(os.Stdout).Encode(struct {
			Phases []rollout.Phase `json:"phases"`
			Total  rollout.Phase   `json:"total"`
		}{all, total})
		return exitcode.OK
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(w, "file\tphase\tcalls\tfirst\tpeak\tinput\tcached\toutput\tpolls\tyields\t")
	row := func(p rollout.Phase, phase string) {
		fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%s\t\n",
			p.File, phase, p.Calls, p.FirstContext, p.Peak, p.Input, p.Cached, p.Output, p.Polls, yields(p.Yields))
	}
	for _, p := range all {
		row(p, fmt.Sprint(p.Index))
	}
	row(total, "")
	_ = w.Flush()
	return exitcode.OK
}

// yields renders {"1000":21,"10000":43} as "1000x21 10000x43", smallest first.
func yields(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if len(keys[i]) != len(keys[j]) {
			return len(keys[i]) < len(keys[j])
		}
		return keys[i] < keys[j]
	})
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%sx%d", k, m[k])
	}
	return strings.Join(parts, " ")
}

// findCodexRollout returns the rollout log Codex wrote for session id under
// sessionsDir (~/.codex/sessions), or "" when there is none. Codex files them
// as YYYY/MM/DD/rollout-<timestamp>-<id>.jsonl, and the id is the session id
// Herdr reports for the pane.
func findCodexRollout(sessionsDir, id string) (string, error) {
	if id == "" || strings.ContainsAny(id, `/\*?[`) {
		return "", nil
	}
	m, err := filepath.Glob(filepath.Join(sessionsDir, "*", "*", "*", "rollout-*-"+id+".jsonl"))
	if err != nil || len(m) == 0 {
		return "", err
	}
	sort.Strings(m)
	return m[len(m)-1], nil
}

// findCodexChildren returns the rollouts of every subagent session spawned,
// at any depth, by session id. Codex records a child's parent in its first
// line (session_meta.payload.parent_thread_id). Children are looked for in
// the parent's day directory and in today's, which covers a task that runs
// past midnight.
func findCodexChildren(sessionsDir, parentPath, id string) []string {
	dirs := map[string]bool{filepath.Dir(parentPath): true}
	dirs[filepath.Join(sessionsDir, time.Now().Format("2006/01/02"))] = true
	parentOf := map[string]string{} // path -> parent id
	idOf := map[string]string{}
	for d := range dirs {
		m, _ := filepath.Glob(filepath.Join(d, "rollout-*.jsonl"))
		for _, p := range m {
			if p == parentPath {
				continue
			}
			if sid, par := rolloutMeta(p); par != "" {
				parentOf[p], idOf[p] = par, sid
			}
		}
	}
	want := map[string]bool{id: true}
	var out []string
	for grew := true; grew; {
		grew = false
		for p, par := range parentOf {
			if want[par] && !want[idOf[p]] {
				want[idOf[p]] = true
				out = append(out, p)
				grew = true
			}
		}
	}
	return out
}

// rolloutMeta reads a rollout's session id and parent from its first line.
func rolloutMeta(path string) (id, parent string) {
	f, err := os.Open(path)
	if err != nil {
		return "", ""
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<16)
	line, err := r.ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return "", ""
	}
	var m struct {
		Type    string `json:"type"`
		Payload struct {
			ID     string `json:"id"`
			Parent string `json:"parent_thread_id"`
		} `json:"payload"`
	}
	if json.Unmarshal(line, &m) != nil || m.Type != "session_meta" {
		return "", ""
	}
	return m.Payload.ID, m.Payload.Parent
}

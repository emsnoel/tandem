package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/emsnoel/tandem/internal/exitcode"
)

// The relay's message shapes, keys in the order the scripts always wrote them.
// tandem-bridge reads them, so a key, a value's type or created's format
// changes only with the bridge.
type relayTask struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	From       string `json:"from"`
	Question   string `json:"question"`
	Task       int    `json:"task"`
	Stuck      bool   `json:"stuck"`
	Review     bool   `json:"review"`
	Verdict    string `json:"verdict"`
	ReportPath string `json:"report_path"`
	Plan       string `json:"plan"`
	Created    string `json:"created"`
}

type relayHandback struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	From       string `json:"from"`
	To         string `json:"to"`
	Question   string `json:"question"`
	Message    string `json:"message"`
	ReportPath string `json:"report_path"`
	Plan       string `json:"plan"`
	Created    string `json:"created"`
}

// relayQuestion has no kind: the bridge reads a message without one as a
// question.
type relayQuestion struct {
	ID       string `json:"id"`
	From     string `json:"from"`
	To       string `json:"to"`
	Question string `json:"question"`
	Created  string `json:"created"`
}

type relayLog struct {
	ID       string `json:"id"`
	TS       string `json:"ts"`
	From     string `json:"from"`
	To       string `json:"to"`
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

const relayUse = "usage: tandem relay write task --mailbox DIR --id ID --from NAME --task N --report PATH --plan PATH [--stuck] [--review --verdict V]\n" +
	"       tandem relay write handback --mailbox DIR --id ID --from NAME --to NAME --message TEXT --report PATH --plan PATH\n" +
	"       tandem relay write question --mailbox DIR --id ID --from NAME --to NAME --question TEXT\n" +
	"       tandem relay log --dir DIR --id ID --from NAME --to NAME --question TEXT --answer TEXT"

// cmdRelay writes the relay's messages for bin/tandem-handback and
// bin/tandem-ask: the mailbox files tandem-bridge delivers, and the Q&A log.
func cmdRelay(args []string) int {
	var name string
	switch {
	case len(args) >= 2 && args[0] == "write":
		name, args = "write "+args[1], args[2:]
	case len(args) >= 1 && args[0] == "log":
		name, args = "log", args[1:]
	default:
		fmt.Fprintln(os.Stderr, relayUse)
		return exitcode.Usage
	}
	fs := flag.NewFlagSet("relay "+name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	str := func(n string) *string { return fs.String(n, "", n) }
	mailbox, dir, id, from, to := str("mailbox"), str("dir"), str("id"), str("from"), str("to")
	task, report, plan, verdict := str("task"), str("report"), str("plan"), str("verdict")
	message, question, answer := str("message"), str("question"), str("answer")
	stuck, review := fs.Bool("stuck", false, "stuck"), fs.Bool("review", false, "review")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *id == "" {
		fmt.Fprintln(os.Stderr, relayUse)
		return exitcode.Usage
	}
	now := time.Now().UTC().Format(time.RFC3339)
	var v any
	path := filepath.Join(*mailbox, "q", *id+".json")
	switch name {
	case "write task":
		n, err := strconv.Atoi(*task)
		if err != nil || n < 0 || *mailbox == "" {
			fmt.Fprintln(os.Stderr, relayUse)
			return exitcode.Usage
		}
		v = relayTask{*id, "task", *from, "task", n, *stuck, *review, *verdict, *report, *plan, now}
	case "write handback":
		v = relayHandback{*id, "handback", *from, *to, "handback", *message, *report, *plan, now}
	case "write question":
		v = relayQuestion{*id, *from, *to, *question, now}
	case "log":
		path = filepath.Join(*dir, *id+".json")
		v = relayLog{*id, now, *from, *to, *question, *answer}
	default:
		fmt.Fprintln(os.Stderr, relayUse)
		return exitcode.Usage
	}
	if (name == "log" && *dir == "") || (name != "log" && *mailbox == "") {
		fmt.Fprintln(os.Stderr, relayUse)
		return exitcode.Usage
	}
	if err := writeFileAtomic(path, v); err != nil {
		fmt.Fprintln(os.Stderr, "tandem relay:", err)
		return exitcode.Internal
	}
	return exitcode.OK
}

// writeFileAtomic writes v as indented JSON through a .tmp file and a rename,
// so a reader never sees half a message.
func writeFileAtomic(path string, v any) error {
	var b bytes.Buffer
	if err := writeIndentedJSON(&b, v); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path+".tmp", b.Bytes(), 0o644); err != nil {
		return err
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		os.Remove(path + ".tmp")
		return err
	}
	return nil
}

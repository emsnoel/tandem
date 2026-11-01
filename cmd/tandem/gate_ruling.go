package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/emsnoel/tandem/internal/config"
	"github.com/emsnoel/tandem/internal/exitcode"
)

// reviewIDPattern is the shape bin/tandem-handback mints. Ids end up in file
// paths, so nothing else is accepted.
var reviewIDPattern = regexp.MustCompile(`^[0-9]{8}-[0-9]{6}-[0-9]+-[0-9]+$`)

// sidecarFlags parses --relay and --id (and --because for rule) and returns
// the path of reviews/ID.json.
func sidecarFlags(name string, args []string, because *string) (string, int) {
	fs := flag.NewFlagSet("gate "+name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	relay := fs.String("relay", "", "the relay mailbox holding reviews/")
	id := fs.String("id", "", "the review id")
	usage := "usage: tandem gate " + name + " --relay DIR --id ID"
	if because != nil {
		fs.StringVar(because, "because", "", "why the review's objections are overruled")
		usage += " --because TEXT"
	}
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *relay == "" || !reviewIDPattern.MatchString(*id) ||
		(because != nil && *because == "") {
		fmt.Fprintln(os.Stderr, usage)
		return "", exitcode.Usage
	}
	return filepath.Join(*relay, "reviews", *id+".json"), exitcode.OK
}

// cmdGateRuling answers bin/tandem-handoff's "what did this review conclude,
// and does a ruling stand on it". It prints the verdict, then the ruling,
// which stands only on a review that is not clean. An unreadable sidecar
// prints two empty fields.
func cmdGateRuling(args []string) int {
	path, rc := sidecarFlags("ruling", args, nil)
	if rc != exitcode.OK {
		return rc
	}
	var meta reviewMeta
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &meta)
	}
	ruling := meta.OverrideReason
	if meta.Verdict == "clean" {
		ruling = ""
	}
	writeFields(os.Stdout, meta.Verdict, ruling)
	return exitcode.OK
}

// cmdGateRule records the planner's ruling in the review sidecar, keeping
// every other key. It writes through a temporary file and a rename.
func cmdGateRule(args []string) int {
	var because string
	path, rc := sidecarFlags("rule", args, &because)
	if rc != exitcode.OK {
		return rc
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ruleFail(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(b, &doc); err != nil || doc == nil {
		return ruleFail(fmt.Errorf("%s is not a JSON object", path))
	}
	reason, _ := json.Marshal(because)
	doc["override_reason"] = reason
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return ruleFail(err)
	}
	if err := os.WriteFile(path+".tmp", append(out, '\n'), 0o644); err != nil {
		return ruleFail(err)
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		os.Remove(path + ".tmp")
		return ruleFail(err)
	}
	return exitcode.OK
}

func ruleFail(err error) int {
	fmt.Fprintln(os.Stderr, "tandem gate rule:", err)
	return exitcode.Internal
}

// cmdConfigBuilderKind prints the kind in the relay's builder record, or
// nothing when there is no readable record.
func cmdConfigBuilderKind(args []string) int {
	fs := flag.NewFlagSet("config builder-kind", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	relay := fs.String("relay", "", "the relay mailbox holding builder.json")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *relay == "" {
		fmt.Fprintln(os.Stderr, "usage: tandem config builder-kind --relay DIR")
		return exitcode.Usage
	}
	if rec, err := config.LoadBuilder(*relay); err == nil && rec.Kind != "" {
		fmt.Println(rec.Kind)
	}
	return exitcode.OK
}

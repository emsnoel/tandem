package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/emsnoel/tandem/internal/exitcode"
)

// The manifest is byte for byte what tandem-init's jq wrote on main.
func TestWriteManifestBytes(t *testing.T) {
	file := filepath.Join(t.TempDir(), ".tandem.json")
	rc, _, errOut := runCapture(t, "config", "write-manifest", "--file", file, "--plans-dir", "docs/plans",
		"--check-command", "make test && echo <ok>", "--conventions-file", "AGENTS.md", "--builder-kind", "codex")
	if rc != exitcode.OK {
		t.Fatalf("rc %d: %s", rc, errOut)
	}
	got, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	want := `{
  "plans_dir": "docs/plans",
  "check_command": "make test && echo <ok>",
  "conventions_file": "AGENTS.md",
  "builder_kind": "codex",
  "prompt_extra": ""
}
`
	if string(got) != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

func TestWriteManifestEmptyValues(t *testing.T) {
	file := filepath.Join(t.TempDir(), ".tandem.json")
	if rc, _, errOut := runCapture(t, "config", "write-manifest", "--file", file); rc != exitcode.OK {
		t.Fatalf("rc %d: %s", rc, errOut)
	}
	got, _ := os.ReadFile(file)
	want := "{\n  \"plans_dir\": \"\",\n  \"check_command\": \"\",\n  \"conventions_file\": \"\",\n  \"builder_kind\": \"\",\n  \"prompt_extra\": \"\"\n}\n"
	if string(got) != want {
		t.Errorf("got %q", got)
	}
}

func TestWriteManifestUnwritable(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	file := filepath.Join(dir, ".tandem.json")
	if rc, _, _ := runCapture(t, "config", "write-manifest", "--file", file, "--plans-dir", "p"); rc != exitcode.Internal {
		t.Errorf("rc %d, want internal", rc)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("left %v behind", entries)
	}
	if rc, _, _ := runCapture(t, "config", "write-manifest", "--plans-dir", "p"); rc != exitcode.Usage {
		t.Errorf("no --file: rc %d, want usage", rc)
	}
}

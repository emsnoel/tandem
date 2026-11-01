package main

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestConfigProjectJSONKeyOrder(t *testing.T) {
	dir := t.TempDir()
	out := captureStdout(t, func() int { return cmdConfig([]string{"project", "--root", dir}) })
	var keys []string
	for _, m := range regexp.MustCompile(`"([a-z_]+)"\s*:`).FindAllStringSubmatch(out, -1) {
		keys = append(keys, m[1])
	}
	want := []string{"root", "plans_dir", "check_command", "conventions_file", "builder_kind", "prompt_extra",
		"check_paths", "task_input_budget", "context_limit", "build_engine", "review_delivery",
		"acceptance_policy", "collaboration", "source"}
	if !reflect.DeepEqual(keys, want) {
		t.Errorf("keys %q\nwant %q", keys, want)
	}
}

func TestConfigProjectFields(t *testing.T) {
	dir := t.TempDir()
	manifest := `{"check_command":"true","prompt_extra":"line one\nline two\n","check_paths":["src dir"]}`
	if err := os.WriteFile(filepath.Join(dir, ".tandem.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() int { return cmdConfig([]string{"project", "--root", dir, "--fields"}) })
	got := strings.Split(out, "\x00")
	want := []string{dir, "", "true", "", "codex", "line one\nline two\n", "3000000", "0", "", "", "", "",
		"manifest", "", "src dir", ""}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("fields %q\nwant   %q", got, want)
	}
}

func TestConfigProjectRootEchoed(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	out := captureStdout(t, func() int { return cmdConfig([]string{"project", "--root", ".", "--fields"}) })
	if root := strings.SplitN(out, "\x00", 2)[0]; root != "." {
		t.Errorf("root %q, want it echoed as given", root)
	}
}

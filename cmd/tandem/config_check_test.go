package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestConfigCheckCommandFields(t *testing.T) {
	dir := t.TempDir()
	manifest := `{"check_command":"make ci","check_paths":["src","a\nb"]}`
	if err := os.WriteFile(filepath.Join(dir, ".tandem.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() int { return cmdConfig([]string{"check-command", "--root", dir, "--fields"}) })
	if got, want := strings.Split(out, "\x00"), []string{"make ci", "src", "a\nb", ""}; !reflect.DeepEqual(got, want) {
		t.Errorf("fields %q, want %q", got, want)
	}
}

func TestConfigCheckCommandJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() int { return cmdConfig([]string{"check-command", "--root", dir}) })
	var got struct {
		Command string   `json:"check_command"`
		Paths   []string `json:"check_paths"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %q", out)
	}
	if got.Command != "go test ./..." || got.Paths == nil || len(got.Paths) != 0 {
		t.Errorf("got %+v, want detected command and an empty array", got)
	}
}

func TestConfigCheckCommandNearestManifest(t *testing.T) {
	root := t.TempDir()
	initGitRepo(t, root)
	pkg := filepath.Join(root, "packages", "foo")
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatal(err)
	}
	for dir, cmd := range map[string]string{root: "root-check", pkg: "pkg-check"} {
		if err := os.WriteFile(filepath.Join(dir, ".tandem.json"), []byte(`{"check_command":"`+cmd+`"}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out := captureStdout(t, func() int {
		return cmdConfig([]string{"check-command", "--root", root, "--from", pkg, "--fields"})
	})
	if out != "pkg-check\x00" {
		t.Errorf("got %q, want the package's own manifest", out)
	}
}

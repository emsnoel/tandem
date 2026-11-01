package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestDetectCheckCommandOrder(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"npm test only", map[string]string{"package.json": `{"scripts":{"test":"jest"}}`}, "npm test"},
		{"npm check wins over test", map[string]string{"package.json": `{"scripts":{"test":"jest","check":"tsc"}}`}, "npm run check"},
		{"npm null script is skipped", map[string]string{"package.json": `{"scripts":{"check":null,"verify":"x"}}`}, "npm run verify"},
		{"npm false script is skipped", map[string]string{"package.json": `{"scripts":{"check":false,"ci":"x"}}`}, "npm run ci"},
		{"package.json without scripts falls through", map[string]string{"package.json": `{}`, "go.mod": "module x\n"}, "go test ./..."},
		{"cargo", map[string]string{"Cargo.toml": ""}, "cargo test"},
		{"go", map[string]string{"go.mod": "module x\n"}, "go test ./..."},
		{"python", map[string]string{"pyproject.toml": ""}, "pytest"},
		{"just test", map[string]string{"Justfile": "build:\n\ttrue\ntest:\n\ttrue\n"}, "just test"},
		{"make check before test", map[string]string{"Makefile": "test:\n\ttrue\ncheck:\n\ttrue\n"}, "make check"},
		{"make without targets", map[string]string{"Makefile": "all:\n\ttrue\n"}, ""},
		{"nothing", map[string]string{}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := DetectCheckCommand(writeFiles(t, c.files)); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestResolveCheckManifestWins(t *testing.T) {
	dir := writeFiles(t, map[string]string{"go.mod": "module x\n", ".tandem.json": `{"check_command":"make ci","check_paths":["a"]}`})
	got := ResolveCheck(dir, filepath.Join(dir, ".tandem.json"))
	want := Check{Command: "make ci", Paths: []string{"a"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestResolveCheckEmptyCommandDetects(t *testing.T) {
	dir := writeFiles(t, map[string]string{"go.mod": "module x\n", ".tandem.json": `{"check_command":""}`})
	if got := ResolveCheck(dir, filepath.Join(dir, ".tandem.json")); got.Command != "go test ./..." {
		t.Errorf("got %q, want detection", got.Command)
	}
}

func TestResolveCheckInvalidJSONDetects(t *testing.T) {
	dir := writeFiles(t, map[string]string{"go.mod": "module x\n", ".tandem.json": `{"check_command":`})
	got := ResolveCheck(dir, filepath.Join(dir, ".tandem.json"))
	if got.Command != "go test ./..." || len(got.Paths) != 0 {
		t.Errorf("got %+v, want detection and no paths", got)
	}
}

func TestResolveCheckMissingManifestDetects(t *testing.T) {
	dir := writeFiles(t, map[string]string{"Cargo.toml": ""})
	if got := ResolveCheck(dir, filepath.Join(dir, ".tandem.json")); got.Command != "cargo test" {
		t.Errorf("got %q, want detection", got.Command)
	}
}

func TestResolveCheckPathsNotArray(t *testing.T) {
	dir := writeFiles(t, map[string]string{".tandem.json": `{"check_command":"true","check_paths":"src"}`})
	if got := ResolveCheck(dir, filepath.Join(dir, ".tandem.json")); len(got.Paths) != 0 {
		t.Errorf("got paths %q, want none", got.Paths)
	}
}

func TestResolveCheckPathsDropsNonStrings(t *testing.T) {
	dir := writeFiles(t, map[string]string{".tandem.json": `{"check_command":"true","check_paths":["a",1,"","b c"]}`})
	got := ResolveCheck(dir, filepath.Join(dir, ".tandem.json"))
	if want := []string{"a", "b c"}; !reflect.DeepEqual(got.Paths, want) {
		t.Errorf("got %q, want %q", got.Paths, want)
	}
}

package pairname

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/paths"
)

func TestSlug(t *testing.T) {
	for in, want := range map[string]string{
		"feat-x": "feat-x", "Feat X": "feat-x", "feat__y": "feat__y", ".tmp": "tmp",
		"a..b": "a-b", "日本": "", "123-fix": "123-fix",
		"a-very-long-worktree-folder-name-indeed": "a-very-long-worktree-fo",
		"abcdefghijklmnopqrstuv-x":                "abcdefghijklmnopqrstuv", // cut, then trailing '-' trimmed
	} {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func noID(t *testing.T) func() (string, error) {
	return func() (string, error) {
		t.Helper()
		t.Fatal("checkout id consulted without a clash")
		return "", nil
	}
}

func TestDeriveMainCheckoutKeepsPlainNames(t *testing.T) {
	got, err := derive(true, "feat-x", nil, noID(t))
	if err != nil {
		t.Fatal(err)
	}
	if want := (Names{"planner", "builder", "reviewer"}); got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestDeriveLinkedWorktreeUsesFolder(t *testing.T) {
	got, err := derive(false, "feat-x", nil, noID(t))
	if err != nil {
		t.Fatal(err)
	}
	if want := (Names{"planner-feat-x", "builder-feat-x", "reviewer-feat-x"}); got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestDeriveFallsBackToCheckoutIDOnClash(t *testing.T) {
	id := func() (string, error) { return "3f9a2c77aabbccddeeff00112233445", nil }
	got, err := derive(false, "feat-x", []string{"other", "feat-x"}, id)
	if err != nil {
		t.Fatal(err)
	}
	if want := (Names{"planner-3f9a2c", "builder-3f9a2c", "reviewer-3f9a2c"}); got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestDeriveFallsBackToCheckoutIDOnEmptySlug(t *testing.T) {
	id := func() (string, error) { return "3f9a2c77aabbccddeeff00112233445", nil }
	got, err := derive(false, "", nil, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Planner != "planner-3f9a2c" {
		t.Errorf("got %+v", got)
	}
}

func TestDeriveNamesFitHerdr(t *testing.T) {
	re := regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
	folder := Slug("0123456789012345678901234567890123456789")
	got, err := derive(false, folder, nil, noID(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{got.Planner, got.Builder, got.Reviewer} {
		if !re.MatchString(n) {
			t.Errorf("%q does not fit herdr", n)
		}
	}
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestDeriveRealWorktrees(t *testing.T) {
	base := t.TempDir()
	main := filepath.Join(base, "main")
	wt := filepath.Join(base, "wt-b")
	plain := filepath.Join(base, "plain")
	for _, d := range []string{main, plain} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	git(t, main, "init", "-q")
	git(t, main, "commit", "-q", "--allow-empty", "-m", "init")
	git(t, main, "worktree", "add", "-q", wt)
	home := t.TempDir()

	for dir, want := range map[string]string{main: "planner", wt: "planner-wt-b", plain: "planner"} {
		got, err := Derive(dir, home)
		if err != nil {
			t.Fatalf("Derive(%s): %v", dir, err)
		}
		if got.Planner != want {
			t.Errorf("Derive(%s).Planner = %q, want %q", dir, got.Planner, want)
		}
	}
}

func TestResolvePrecedence(t *testing.T) {
	base := t.TempDir()
	main := filepath.Join(base, "main")
	wt := filepath.Join(base, "wt-b")
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, main, "init", "-q")
	git(t, main, "commit", "-q", "--allow-empty", "-m", "init")
	git(t, main, "worktree", "add", "-q", wt)
	home := t.TempDir()

	// Derived only.
	got, err := Resolve(wt, home, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if got != (Names{"planner-wt-b", "builder-wt-b", "reviewer-wt-b"}) {
		t.Fatalf("derived: %+v", got)
	}
	// The record beats derived.
	relay := paths.Relay(wt)
	if err := os.MkdirAll(relay, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(relay, RecordName), []byte(`{"planner":"p2","builder":"b2"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, _ = Resolve(wt, home, func(string) string { return "" })
	if got != (Names{"p2", "b2", "reviewer-wt-b"}) {
		t.Fatalf("record: %+v", got)
	}
	// A subdirectory finds the record at the toplevel.
	sub := filepath.Join(wt, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	got, _ = Resolve(sub, home, func(string) string { return "" })
	if got != (Names{"p2", "b2", "reviewer-wt-b"}) {
		t.Fatalf("subdir record: %+v", got)
	}
	// Env beats the record, per role.
	env := map[string]string{"HERDR_BUILDER_NAME": "bob"}
	got, _ = Resolve(wt, home, func(k string) string { return env[k] })
	if got.Builder != "bob" || got.Planner != "p2" {
		t.Fatalf("env: %+v", got)
	}
}

func TestResolveRecordBeatsDerivedAndEnvBeatsRecord(t *testing.T) {
	rec := Names{Planner: "p2", Builder: "b2"}
	got := merge(Names{"p-env", "", ""}, rec, Names{"pd", "bd", "rd"})
	if want := (Names{"p-env", "b2", "rd"}); got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestReadRecordDropsInvalidNames(t *testing.T) {
	relay := t.TempDir()
	body := `{"planner":"Bad Name","builder":"b2"}`
	if err := os.WriteFile(filepath.Join(relay, RecordName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, ok, err := ReadRecord(relay)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if got.Planner != "" || got.Builder != "b2" {
		t.Errorf("got %+v", got)
	}
	if _, ok, err := ReadRecord(t.TempDir()); ok || err != nil {
		t.Errorf("missing file: ok=%v err=%v", ok, err)
	}
}

func TestDeriveRealClashUsesCheckoutID(t *testing.T) {
	base := t.TempDir()
	main := filepath.Join(base, "main")
	a, b := filepath.Join(base, "A b"), filepath.Join(base, "a-b")
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, main, "init", "-q")
	git(t, main, "commit", "-q", "--allow-empty", "-m", "init")
	git(t, main, "worktree", "add", "-q", "-b", "br-a", a)
	git(t, main, "worktree", "add", "-q", "-b", "br-b", b)
	sub := filepath.Join(a, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	na, err := Derive(sub, home)
	if err != nil {
		t.Fatal(err)
	}
	nb, err := Derive(b, home)
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`^planner-[0-9a-f]{6}$`)
	if !re.MatchString(na.Planner) || !re.MatchString(nb.Planner) {
		t.Errorf("want 6-hex suffixes, got %q and %q", na.Planner, nb.Planner)
	}
	if na.Planner == nb.Planner {
		t.Errorf("both worktrees got %q", na.Planner)
	}
}

func TestReadRecordCorruptNamesTheFile(t *testing.T) {
	relay := t.TempDir()
	if err := os.WriteFile(filepath.Join(relay, RecordName), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := ReadRecord(relay)
	if err == nil || !strings.Contains(err.Error(), filepath.Join(relay, RecordName)) {
		t.Errorf("err = %v", err)
	}
}

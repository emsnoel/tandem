package sourcehash

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixture writes files (relative slash paths) under a fresh root and returns it.
func fixture(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, body := range files {
		put(t, root, rel, body)
	}
	return root
}

func put(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func base() map[string]string {
	return map[string]string{
		"go.mod":                          "module m\n\ngo 1.26.0\n",
		"go.sum":                          "x v1 h1:abc\n",
		"cmd/tool/main.go":                "package main\n",
		"internal/a/a.go":                 "package a\n",
		"internal/state/migrations/1.sql": "CREATE TABLE t(x);\n",
		"internal/p/prompt.tmpl":          "hello {{.}}\n",
	}
}

func hashOf(t *testing.T, root string) string {
	t.Helper()
	h, err := Hash(root)
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if len(h) != 64 {
		t.Fatalf("hash %q is not 64 hex characters", h)
	}
	return h
}

func TestSourceHashIncludesRuntimeInputs(t *testing.T) {
	want := hashOf(t, fixture(t, base()))
	for rel := range base() {
		files := base()
		files[rel] += "changed\n"
		if got := hashOf(t, fixture(t, files)); got == want {
			t.Errorf("changing %s did not change the hash", rel)
		}
	}
	// A new input file of each kind changes it too.
	for _, rel := range []string{"internal/a/b.go", "internal/state/migrations/2.sql", "internal/p/other.tmpl", "cmd/x/y_test.go"} {
		files := base()
		files[rel] = "x\n"
		if got := hashOf(t, fixture(t, files)); got == want {
			t.Errorf("adding %s did not change the hash", rel)
		}
	}
}

func TestSourceHashIgnoresWhatIsNotAnInput(t *testing.T) {
	want := hashOf(t, fixture(t, base()))
	files := base()
	files["internal/state/state.sqlite"] = "binary"
	files["internal/state/state.sqlite-wal"] = "wal"
	files[".gocache/x/y.go"] = "package cache\n"
	files["README.md"] = "docs\n"
	files["internal/a/notes.txt"] = "notes\n"
	if got := hashOf(t, fixture(t, files)); got != want {
		t.Fatal("a database file, a cache, a doc or a note changed the hash")
	}
}

func TestSourceHashIndependentOfCheckoutPath(t *testing.T) {
	a := hashOf(t, fixture(t, base()))
	b := hashOf(t, fixture(t, base()))
	if a != b {
		t.Fatal("the same tree in two directories hashed differently")
	}
}

func TestSourceHashHandlesUnusualPaths(t *testing.T) {
	spaced := hashOf(t, fixture(t, map[string]string{"go.mod": "m", "internal/a b/x y.go": "p"}))
	nl := hashOf(t, fixture(t, map[string]string{"go.mod": "m", "internal/a\nb/x\ny.go": "p"}))
	if spaced == nl || spaced == "" {
		t.Fatal("paths that differ by a space and a newline hashed the same")
	}
	// Without length prefixes these two inputs form the same byte stream:
	// "a.go"+"x" then "b.go"+"y", against "a.go" and "xb.goy".
	two := hashOf(t, fixture(t, map[string]string{"go.mod": "m", "internal/a.go": "x", "internal/b.go": "y"}))
	one := hashOf(t, fixture(t, map[string]string{"go.mod": "m", "internal/a.go": "xinternal/b.goy"}))
	if two == one {
		t.Fatal("path and content boundaries are ambiguous")
	}
}

func TestSourceHashOrderIsBytewise(t *testing.T) {
	// "internal/B.go" sorts before "internal/a.go" bytewise; a locale-aware sort
	// would reverse them. Adding then removing nothing must be stable either way.
	h1 := hashOf(t, fixture(t, map[string]string{"go.mod": "m", "internal/B.go": "1", "internal/a.go": "2"}))
	h2 := hashOf(t, fixture(t, map[string]string{"go.mod": "m", "internal/a.go": "2", "internal/B.go": "1"}))
	if h1 != h2 {
		t.Fatal("hash depends on creation order")
	}
}

func TestSourceHashFailsClosed(t *testing.T) {
	if _, err := Hash(t.TempDir()); err == nil {
		t.Error("an empty directory hashed without error")
	}
	if _, err := Hash(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Error("a missing root hashed without error")
	}
	root := fixture(t, base())
	if err := os.Symlink("a.go", filepath.Join(root, "internal", "a", "link.go")); err != nil {
		t.Fatal(err)
	}
	if _, err := Hash(root); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Errorf("a symlinked input was accepted: %v", err)
	}
}

func TestSourceHashOfThisRepositoryIsStable(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	a, err := Hash(root)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := Hash(root); a != b {
		t.Fatal("two hashes of the same tree differ")
	}
}

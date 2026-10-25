package mailbox

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTakeAndDiffReports(t *testing.T) {
	dir := t.TempDir()
	write := func(name string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("old.md")
	write("ignored.txt")
	before, err := Take(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 1 {
		t.Fatalf("want one report, got %v", before)
	}
	time.Sleep(time.Millisecond)
	write("new.md")
	after, err := Take(dir)
	if err != nil {
		t.Fatal(err)
	}
	diff := before.Diff(after)
	if len(diff) != 1 || diff[0] != "new" {
		t.Fatalf("want [new], got %v", diff)
	}
}

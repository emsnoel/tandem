package vcs

import (
	"strings"
	"testing"
)

func TestTaskDiffIncludesNewAndModifiedFilesOnly(t *testing.T) {
	root := gitRepo(t)
	write(t, root, "a.txt", "one\n")
	write(t, root, "other.txt", "keep\n")
	commitAll(t, root)
	write(t, root, "a.txt", "one\ntwo\n")
	write(t, root, "new.txt", "brand new\n")
	write(t, root, "other.txt", "changed by someone else\n")

	got, err := TaskDiff(root, []string{"a.txt", "new.txt"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"+two", "+brand new", "new.txt"} {
		if !strings.Contains(got, want) {
			t.Errorf("diff misses %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "someone else") {
		t.Errorf("diff carries a file outside the task:\n%s", got)
	}
}

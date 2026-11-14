package controller

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// The teardown gate reads these to know a person ended the relay's run. A file
// that only looks like a set-aside marker must not count.
func TestArchivedRunsListsOnlySetAsideMarkers(t *testing.T) {
	relay := t.TempDir()
	if ids, err := ArchivedRuns(relay); err != nil || len(ids) != 0 {
		t.Fatalf("an empty relay: %v %v", ids, err)
	}
	for _, name := range []string{ArchivedRunFile(hex32(301)), ArchivedRunFile("not-a-run"), ActiveRunFile, "active-run.json.bak"} {
		if err := os.WriteFile(filepath.Join(relay, name), []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ids, err := ArchivedRuns(relay)
	if err != nil || !reflect.DeepEqual(ids, []string{hex32(301)}) {
		t.Fatalf("got %v %v, want only %s", ids, err, hex32(301))
	}
}

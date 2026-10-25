package snapshot

import (
	"reflect"
	"testing"
)

func TestChildrenNamesTheContentOfSnapshotsAndPatches(t *testing.T) {
	manifest := `{"version":1,"complete":true,"errors":[],"entries":[{"path":"a","kind":"file","executable":false,"content_hash":"bb","size":1},{"path":"b","kind":"file","executable":false,"content_hash":"aa","size":1},{"path":"c","kind":"file","executable":false,"content_hash":"aa","size":1}]}`
	if got := Children([]byte(manifest)); !reflect.DeepEqual(got, []string{"aa", "bb"}) {
		t.Errorf("manifest: %v", got)
	}
	patch := `{"version":1,"start_snapshot_hash":"s","end_snapshot_hash":"e","complete":true,"errors":[],"changes":[{"path":"a","op":"modify","before_hash":"cc","after_hash":"dd"},{"path":"b","op":"add","after_hash":"ee"},{"path":"c","op":"delete","before_hash":"cc"}]}`
	if got := Children([]byte(patch)); !reflect.DeepEqual(got, []string{"cc", "dd", "ee"}) {
		t.Errorf("patch: %v", got)
	}
	for name, raw := range map[string]string{
		"plain text":      "just some log output\n",
		"another version": `{"version":2,"entries":[{"path":"a","content_hash":"zz"}]}`,
		"no version":      `{"entries":[{"path":"a","content_hash":"zz"}]}`,
		"a JSON array":    `[1,2,3]`,
		"empty":           ``,
	} {
		if got := Children([]byte(raw)); len(got) != 0 {
			t.Errorf("%s names %v", name, got)
		}
	}
}

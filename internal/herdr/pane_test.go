package herdr

import "testing"

func TestSplitPaneID(t *testing.T) {
	got, err := SplitPaneID([]byte(`{"result":{"pane":{"pane_id":"w1:p7"}}}`))
	if err != nil || got != "w1:p7" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestSplitPaneIDMissing(t *testing.T) {
	for _, in := range []string{`{"result":{}}`, `not json`, `{"result":{"pane":{"pane_id":""}}}`} {
		if got, err := SplitPaneID([]byte(in)); err == nil {
			t.Errorf("%s: got %q, want an error", in, got)
		}
	}
}

const layout = `{"result":{"layout":{"panes":[
 {"pane_id":"w1:p1","rect":{"width":200,"height":50}},
 {"pane_id":"w1:p2","rect":{"width":0,"height":50}}]}}}`

func TestPaneSizeFindsPane(t *testing.T) {
	w, h, ok := PaneSize([]byte(layout), "w1:p1")
	if !ok || w != 200 || h != 50 {
		t.Fatalf("got %d x %d, %v", w, h, ok)
	}
	if _, _, ok := PaneSize([]byte(layout), "w1:p9"); ok {
		t.Fatal("an unlisted pane has a size")
	}
}

func TestPaneSizeZeroIsUnknown(t *testing.T) {
	if _, _, ok := PaneSize([]byte(layout), "w1:p2"); ok {
		t.Fatal("a zero width counts as known")
	}
	if _, _, ok := PaneSize([]byte("garbage"), "w1:p1"); ok {
		t.Fatal("garbage has a size")
	}
}

func TestSplitDirection(t *testing.T) {
	for _, c := range []struct {
		w, h int
		want string
	}{{200, 50, "right"}, {100, 50, "down"}, {101, 50, "right"}} {
		if got := SplitDirection(c.w, c.h); got != c.want {
			t.Errorf("%dx%d: got %s, want %s", c.w, c.h, got, c.want)
		}
	}
}

// Herdr 0.9.5 labels an unnamed tab with its 1-based position in the
// workspace and renumbers it when an earlier tab closes. Its "number" is a
// global counter (84 for the third tab, seen live), so it says nothing about
// the label.
func TestTabIsDefault(t *testing.T) {
	const list = `{"result":{"tabs":[` +
		`{"tab_id":"w8:tN","label":"statusbar","number":21},` +
		`{"tab_id":"w8:t1Y","label":"tandem changes fable","number":62},` +
		`{"tab_id":"w8:t2M","label":"3","number":84}]}}`
	for name, c := range map[string]struct {
		tab  string
		want bool
	}{
		"a label equal to its position is the default": {`{"result":{"tab":{"tab_id":"w8:t2M","label":"3","number":84}}}`, true},
		"a label a person chose is not":                {`{"result":{"tab":{"tab_id":"w8:t2M","label":"mine","number":84}}}`, false},
		"a label equal to its number alone is not":     {`{"result":{"tab":{"tab_id":"w8:t2M","label":"84","number":84}}}`, false},
		"another position's number is not":             {`{"result":{"tab":{"tab_id":"w8:t2M","label":"2","number":84}}}`, false},
	} {
		got, err := TabIsDefault([]byte(c.tab), []byte(list))
		if err != nil || got != c.want {
			t.Errorf("%s: got %v, %v", name, got, err)
		}
	}
	if _, err := TabIsDefault([]byte(`{}`), []byte(list)); err == nil {
		t.Error("a missing tab is not an error")
	}
	if _, err := TabIsDefault([]byte(`{"result":{"tab":{"tab_id":"w8:t9","label":"3"}}}`), []byte(list)); err == nil {
		t.Error("a tab missing from its workspace's list is not an error")
	}
}

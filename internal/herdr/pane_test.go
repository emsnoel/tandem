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

func TestTabIsDefault(t *testing.T) {
	for in, want := range map[string]bool{
		`{"result":{"tab":{"label":"3","number":3}}}`:    true,
		`{"result":{"tab":{"label":"mine","number":3}}}`: false,
	} {
		got, err := TabIsDefault([]byte(in))
		if err != nil || got != want {
			t.Errorf("%s: got %v, %v", in, got, err)
		}
	}
	if _, err := TabIsDefault([]byte(`{}`)); err == nil {
		t.Error("a missing tab is not an error")
	}
}

package mailwatch

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// These use real folders: faking fsnotify would test nothing. Every wait has
// a deadline; quiet checks give a stray event 300 ms to show up.

func open(t *testing.T) *Watch {
	t.Helper()
	w, err := New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	return w
}

func mkdir(t *testing.T, parts ...string) string {
	t.Helper()
	d := filepath.Join(parts...)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	return d
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func rename(t *testing.T, from, to string) {
	t.Helper()
	if err := os.Rename(from, to); err != nil {
		t.Fatal(err)
	}
}

func wake(t *testing.T, w *Watch) Wake {
	t.Helper()
	select {
	case wk := <-w.Wakes():
		return wk
	case err := <-w.Errors():
		t.Fatalf("watch error: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("no wake within 2 s")
	}
	return Wake{}
}

func quiet(t *testing.T, w *Watch, what string) {
	t.Helper()
	select {
	case wk := <-w.Wakes():
		t.Fatalf("%s woke the watch: %+v", what, wk)
	case <-time.After(300 * time.Millisecond):
	}
}

// The writers put a message in place with `mv x.json.tmp x.json`.
func TestAMessageRenamedIntoAQueueWakesIt(t *testing.T) {
	q := mkdir(t, t.TempDir(), "q")
	w := open(t)
	if err := w.AddQueue(q); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(q, "m1.json.tmp"), "{")
	quiet(t, w, "a .tmp file being written")
	rename(t, filepath.Join(q, "m1.json.tmp"), filepath.Join(q, "m1.json"))
	if wk := wake(t, w); wk.Kind != "queued" || wk.Path != filepath.Join(q, "m1.json") {
		t.Fatalf("wake %+v", wk)
	}
}

// The bridge claims a message with `mv -n x.json x.json.taken`.
func TestAClaimDoesNotWakeIt(t *testing.T) {
	q := mkdir(t, t.TempDir(), "q")
	write(t, filepath.Join(q, "m1.json"), "{}")
	w := open(t)
	if err := w.AddQueue(q); err != nil {
		t.Fatal(err)
	}
	rename(t, filepath.Join(q, "m1.json"), filepath.Join(q, "m1.json.taken"))
	quiet(t, w, "a claim")
}

func TestWaitingFindsOnlyAMessageAlreadyQueued(t *testing.T) {
	q := mkdir(t, t.TempDir(), "q")
	for _, n := range []string{"a.json.tmp", "b.json.taken", ".c.json"} {
		write(t, filepath.Join(q, n), "{}")
	}
	w := open(t)
	if err := w.AddQueue(q); err != nil {
		t.Fatal(err)
	}
	if p, ok := w.Waiting(); ok {
		t.Fatalf("Waiting found %s among temp files, claims and dotfiles", p)
	}
	write(t, filepath.Join(q, "d.json"), "{}")
	if p, ok := w.Waiting(); !ok || p != filepath.Join(q, "d.json") {
		t.Fatalf("Waiting = %q %v", p, ok)
	}
}

// The run marker and the nudge are watched by name. Other files in the same
// folder (the bridge's own log, the builder record) are not.
func TestANamedFileWrittenAfterArmingWakesIt(t *testing.T) {
	relay := t.TempDir()
	w := open(t)
	marker := filepath.Join(relay, "active-run.json")
	if err := w.AddFile(marker, "run"); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(relay, "bridge.log"), "line\n")
	write(t, filepath.Join(relay, "builder.json"), "{}")
	quiet(t, w, "another file in the folder")
	write(t, marker+".tmp", "{}")
	rename(t, marker+".tmp", marker)
	if wk := wake(t, w); wk.Kind != "run" || wk.Path != marker {
		t.Fatalf("wake %+v", wk)
	}
}

// A durable run's queue is added once the caller knows the run.
func TestAQueueAddedLaterWakesIt(t *testing.T) {
	relay := t.TempDir()
	w := open(t)
	if err := w.AddQueue(mkdir(t, relay, "q")); err != nil {
		t.Fatal(err)
	}
	rq := mkdir(t, relay, "durable", "r1", "q")
	if err := w.AddQueue(rq); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(rq, "e1.json.tmp"), "{}")
	rename(t, filepath.Join(rq, "e1.json.tmp"), filepath.Join(rq, "e1.json"))
	if wk := wake(t, w); wk.Kind != "queued" || wk.Path != filepath.Join(rq, "e1.json") {
		t.Fatalf("wake %+v", wk)
	}
}

func TestAMissingFolderIsAnError(t *testing.T) {
	w := open(t)
	if err := w.AddQueue(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("AddQueue on a missing folder succeeded")
	}
	if err := w.AddFile(filepath.Join(t.TempDir(), "nope", "nudge"), "nudge"); err == nil {
		t.Fatal("AddFile in a missing folder succeeded")
	}
}

// macOS touches a new file's attributes up to about 250 ms after creating it.
// That CHMOD must not wake the watch, or the bridge would wake on its own nudge
// every pass.
func TestAnAttributeChangeDoesNotWakeIt(t *testing.T) {
	dir := t.TempDir()
	nudge := filepath.Join(dir, "nudge")
	write(t, nudge, "1\n")
	w := open(t)
	if err := w.AddFile(nudge, "nudge"); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(nudge, 0o600); err != nil {
		t.Fatal(err)
	}
	quiet(t, w, "a chmod of the named file")
}

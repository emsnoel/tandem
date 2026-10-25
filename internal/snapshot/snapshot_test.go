package snapshot

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/artifact"
)

var ctx = context.Background()

func sum(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "protocol.file.allow=always"}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fixture is the layout from the verification plan: two tracked files and a
// plan, committed.
func fixture(t *testing.T) (root string, store *artifact.Store) {
	t.Helper()
	root = filepath.Join(t.TempDir(), "repo")
	os.MkdirAll(root, 0o755)
	git(t, root, "init", "-q")
	write(t, root, "src/a.txt", "base-a\n")
	write(t, root, "src/b.txt", "base-b\n")
	write(t, root, "plan.md", "# plan\n")
	git(t, root, "add", ".")
	git(t, root, "commit", "-qm", "init")
	st, err := artifact.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	return root, st
}

func capture(t *testing.T, root string, st *artifact.Store) Manifest {
	t.Helper()
	m, err := Capture(ctx, root, st)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	return m
}

func complete(t *testing.T, m Manifest) Manifest {
	t.Helper()
	if !m.Complete || len(m.Errors) != 0 {
		t.Fatalf("manifest incomplete: %v", m.Errors)
	}
	return m
}

func diff(t *testing.T, a, b Manifest) Patch {
	t.Helper()
	p, err := Diff(a, b)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	return p
}

func changed(p Patch) []string {
	var out []string
	for _, c := range p.Changes {
		out = append(out, c.Entry())
	}
	return out
}

func entryOf(m Manifest, path string) (Entry, bool) {
	for _, e := range m.Entries {
		if e.PathString() == path {
			return e, true
		}
	}
	return Entry{}, false
}

func TestSnapshotIncludesCleanTrackedAndUntrackedButNotIgnoredFiles(t *testing.T) {
	root, st := fixture(t)
	write(t, root, ".gitignore", "ignored.log\n.tandem-relay/\n")
	git(t, root, "add", ".gitignore")
	git(t, root, "commit", "-qm", "ignore")
	write(t, root, "untracked.txt", "new\n")
	write(t, root, "ignored.log", "noise\n")
	write(t, root, ".tandem-relay/q/1.json", "{}")
	m := complete(t, capture(t, root, st))

	var got []string
	for _, e := range m.Entries {
		got = append(got, e.PathString())
	}
	want := []string{".gitignore", "plan.md", "src/a.txt", "src/b.txt", "untracked.txt"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("entries %v, want %v (clean tracked files count; ignored files and the relay do not)", got, want)
	}
	if e, _ := entryOf(m, "src/a.txt"); e.ContentHash != sum("base-a\n") || e.Kind != KindFile || e.Size != 7 || e.Executable {
		t.Fatalf("entry %+v", e)
	}
	// The hashed bytes are real artifacts.
	c, err := st.Read(ctx, sum("base-a\n"), 0, 0)
	if err != nil || string(c.Data) != "base-a\n" {
		t.Fatalf("content artifact: %v %q", err, c.Data)
	}
}

func TestSnapshotIsDeterministicAndLeavesTheIndexAlone(t *testing.T) {
	root, st := fixture(t)
	write(t, root, "untracked.txt", "x")
	indexBefore, _ := os.ReadFile(filepath.Join(root, ".git", "index"))
	a, b := capture(t, root, st), capture(t, root, st)
	if a.Hash == "" || a.Hash != b.Hash {
		t.Fatalf("hashes %s %s", a.Hash, b.Hash)
	}
	if !sort.SliceIsSorted(a.Entries, func(i, j int) bool { return bytes.Compare(a.Entries[i].RawPath(), a.Entries[j].RawPath()) < 0 }) {
		t.Fatal("entries are not sorted by raw path bytes")
	}
	indexAfter, _ := os.ReadFile(filepath.Join(root, ".git", "index"))
	if !bytes.Equal(indexBefore, indexAfter) {
		t.Fatal("capturing touched the git index")
	}
	if status := git(t, root, "status", "--porcelain"); strings.TrimSpace(status) != "?? untracked.txt" {
		t.Fatalf("status %q", status)
	}
	// The manifest is stored as an artifact named by its hash.
	if _, err := st.Read(ctx, a.Hash, 0, 1); err != nil {
		t.Fatalf("manifest artifact: %v", err)
	}
	var doc struct{ Entries []Entry }
	all, _ := st.ReadAll(ctx, a.Hash, 1<<20)
	if err := json.Unmarshal(all, &doc); err != nil || len(doc.Entries) != len(a.Entries) || !bytes.HasSuffix(all, []byte("}\n")) {
		t.Fatalf("stored manifest: %v %q", err, all)
	}
}

// The second task starts from a checkout the first task left dirty, and its
// patch must hold only its own delta.
func TestSnapshotSecondTaskExcludesFirstTaskChanges(t *testing.T) {
	root, st := fixture(t)
	write(t, root, "src/b.txt", "user-dirty\n") // dirty before any task
	start1 := complete(t, capture(t, root, st))
	write(t, root, "src/a.txt", "task-one\n")
	end1 := complete(t, capture(t, root, st)) // also task 2's start
	write(t, root, "src/a.txt", "task-two\n")
	end2 := complete(t, capture(t, root, st))

	p1 := diff(t, start1, end1)
	if got := changed(p1); len(got) != 1 || got[0] != "src/a.txt" {
		t.Fatalf("task 1 changed %v", got)
	}
	p2 := diff(t, end1, end2)
	if got := changed(p2); len(got) != 1 || got[0] != "src/a.txt" {
		t.Fatalf("task 2 changed %v, want only src/a.txt", got)
	}
	c := p2.Changes[0]
	if c.Op != OpModify || c.BeforeHash != sum("task-one\n") || c.AfterHash != sum("task-two\n") {
		t.Fatalf("task 2 change %+v: it must run from task-one to task-two", c)
	}
	if !p2.Complete || p2.StartSnapshotHash != end1.Hash || p2.EndSnapshotHash != end2.Hash || len(p2.Hash) != 64 {
		t.Fatalf("patch %+v", p2)
	}

	text, err := RenderPatch(ctx, st, p2)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := st.ReadAll(ctx, text, 1<<20)
	for _, want := range []string{"-task-one", "+task-two", "src/a.txt"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("rendered patch lacks %q:\n%s", want, body)
		}
	}
	for _, leak := range []string{"base-a", "user-dirty", "b.txt"} {
		if strings.Contains(string(body), leak) {
			t.Errorf("rendered patch leaks earlier work %q:\n%s", leak, body)
		}
	}
	// A HEAD-relative diff cannot make that separation, which is the reason it
	// stays on the legacy route.
	if head := git(t, root, "diff", "HEAD", "--stat"); !strings.Contains(head, "b.txt") || !strings.Contains(head, "a.txt") {
		t.Fatalf("expected the HEAD diff to mix both files: %s", head)
	}
}

func TestSnapshotModesSymlinksAndBinary(t *testing.T) {
	root, st := fixture(t)
	write(t, root, "gone.txt", "doomed\n")
	write(t, root, "blob.bin", "AB\x00\x01CD")
	write(t, root, "retyped", "plain file\n")
	git(t, root, "add", ".")
	git(t, root, "commit", "-qm", "more")
	outside := filepath.Join(t.TempDir(), "outside.txt")
	os.WriteFile(outside, []byte("outside v1"), 0o644)
	os.Symlink(outside, filepath.Join(root, "link"))
	start := complete(t, capture(t, root, st))

	os.Chmod(filepath.Join(root, "src/a.txt"), 0o755)
	write(t, root, "blob.bin", "AB\x00\x02CD")
	os.Remove(filepath.Join(root, "gone.txt"))
	write(t, root, "added.txt", "added\n")
	os.Remove(filepath.Join(root, "retyped"))
	os.Symlink("src/a.txt", filepath.Join(root, "retyped"))
	os.WriteFile(outside, []byte("outside v2, changed behind the link"), 0o644) // must not be followed
	end := complete(t, capture(t, root, st))

	p := diff(t, start, end)
	byPath := map[string]Change{}
	for _, c := range p.Changes {
		byPath[c.Entry()] = c
	}
	want := []string{"added.txt", "blob.bin", "gone.txt", "retyped", "src/a.txt"}
	if got := changed(p); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("changed %v, want %v (the symlink target's content must not count)", got, want)
	}
	if c := byPath["src/a.txt"]; c.Op != OpMode || c.BeforeExecutable || !c.AfterExecutable || c.BeforeHash != c.AfterHash {
		t.Errorf("mode change: %+v", c)
	}
	if c := byPath["gone.txt"]; c.Op != OpDelete || c.BeforeHash != sum("doomed\n") || c.AfterHash != "" {
		t.Errorf("deletion: %+v", c)
	}
	if c := byPath["added.txt"]; c.Op != OpAdd || c.AfterHash != sum("added\n") || c.BeforeHash != "" {
		t.Errorf("addition: %+v", c)
	}
	if c := byPath["blob.bin"]; c.Op != OpModify || c.BeforeHash != sum("AB\x00\x01CD") || c.AfterHash != sum("AB\x00\x02CD") {
		t.Errorf("binary edit: %+v", c)
	}
	if c := byPath["retyped"]; c.Op != OpModify || c.BeforeKind != KindFile || c.AfterKind != KindSymlink || c.AfterHash != sum("src/a.txt") {
		t.Errorf("type change: %+v", c)
	}
	if e, ok := entryOf(start, "link"); !ok || e.Kind != KindSymlink || e.ContentHash != sum(outside) {
		t.Errorf("symlink entry %+v (the target text is hashed, never the target's content)", e)
	}

	text, err := RenderPatch(ctx, st, p)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := st.ReadAll(ctx, text, 1<<20)
	s := string(body)
	for _, want := range []string{"Binary files differ", sum("AB\x00\x01CD"), sum("AB\x00\x02CD"), "old mode 100644", "new mode 100755", "deleted file", "new file", "symlink"} {
		if !strings.Contains(s, want) {
			t.Errorf("rendered patch lacks %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "\x00") {
		t.Error("binary content was printed into the text patch")
	}
}

func TestSnapshotReadErrorIsIncomplete(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads files regardless of mode")
	}
	root, st := fixture(t)
	write(t, root, "secret.txt", "cannot read me")
	os.Chmod(filepath.Join(root, "secret.txt"), 0o000)
	t.Cleanup(func() { os.Chmod(filepath.Join(root, "secret.txt"), 0o644) })

	m := capture(t, root, st)
	if m.Complete || len(m.Errors) == 0 || !strings.Contains(strings.Join(m.Errors, "\n"), "secret.txt") {
		t.Fatalf("complete=%v errors=%v: an unreadable file must be named and make the manifest incomplete", m.Complete, m.Errors)
	}
	if _, ok := entryOf(m, "secret.txt"); ok {
		t.Fatal("an unreadable file was recorded as if it had content")
	}
	// An incomplete manifest never yields a complete patch, even against itself.
	p := diff(t, m, m)
	if p.Complete || len(p.Errors) == 0 {
		t.Fatalf("patch from incomplete manifests: %+v", p)
	}
	// A permission failure is not a deletion: once readable, the file is simply there.
	os.Chmod(filepath.Join(root, "secret.txt"), 0o644)
	good := complete(t, capture(t, root, st))
	if e, ok := entryOf(good, "secret.txt"); !ok || e.ContentHash != sum("cannot read me") {
		t.Fatalf("entry %+v", e)
	}
}

func TestSnapshotConcurrentMutationRetriesOnce(t *testing.T) {
	root, st := fixture(t)
	write(t, root, "busy.txt", "v0")
	attempts := map[int]int{}
	n := 0
	mutate := func(onAttempts ...int) func(path string, attempt int) {
		return func(path string, attempt int) {
			if path != "busy.txt" {
				return
			}
			attempts[attempt]++
			for _, a := range onAttempts {
				if a == attempt {
					n++
					write(t, root, "busy.txt", strings.Repeat("x", n+10)) // changes size and mtime
				}
			}
		}
	}

	m, err := CaptureWith(ctx, root, st, Options{afterRead: mutate(1)})
	if err != nil {
		t.Fatal(err)
	}
	if !m.Complete {
		t.Fatalf("a file that settled on the retry must capture: %v", m.Errors)
	}
	if e, _ := entryOf(m, "busy.txt"); e.ContentHash != sum(strings.Repeat("x", 11)) {
		t.Fatalf("the retry captured stale content: %+v", e)
	}
	if attempts[1] != 1 || attempts[2] != 1 || attempts[3] != 0 {
		t.Fatalf("attempts %v, want one first read and one retry", attempts)
	}

	attempts, n = map[int]int{}, 0
	m, err = CaptureWith(ctx, root, st, Options{afterRead: mutate(1, 2)})
	if err != nil {
		t.Fatal(err)
	}
	if m.Complete || !strings.Contains(strings.Join(m.Errors, "\n"), "busy.txt") {
		t.Fatalf("a file that changed during both reads must be incomplete: complete=%v %v", m.Complete, m.Errors)
	}
	if attempts[3] != 0 || attempts[1]+attempts[2] != 2 {
		t.Fatalf("attempts %v: capture must try twice and stop", attempts)
	}
	if _, ok := entryOf(m, "busy.txt"); ok {
		t.Fatal("an unstable file was recorded")
	}
}

func TestSnapshotNonUTF8PathRoundTrips(t *testing.T) {
	raw := []byte("dir/caf\xe9\xff.txt")
	e := newEntry(raw)
	if e.Path != "" || e.PathB64 == "" {
		t.Fatalf("a non-UTF8 path must use path_b64: %+v", e)
	}
	e.Kind, e.ContentHash = KindFile, sum("x")
	encoded, err := json.Marshal(Manifest{Complete: true, Entries: []Entry{e}})
	if err != nil {
		t.Fatal(err)
	}
	var back Manifest
	if err := json.Unmarshal(encoded, &back); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(back.Entries[0].RawPath(), raw) {
		t.Fatalf("round trip %q != %q", back.Entries[0].RawPath(), raw)
	}
	if !strings.Contains(string(encoded), "path_b64") || strings.Contains(string(encoded), "\ufffd") {
		t.Fatalf("manifest encoding replaced bytes: %s", encoded)
	}
	// A valid UTF-8 path stays human readable.
	if g := newEntry([]byte("src/a.txt")); g.Path != "src/a.txt" || g.PathB64 != "" {
		t.Fatalf("%+v", g)
	}

	// Through a real filesystem, where it allows such names (macOS does not).
	root, st := fixture(t)
	if err := os.WriteFile(filepath.Join(root, string(raw[:4])+"\xe9\xff.txt"), []byte("odd"), 0o644); err != nil {
		t.Skipf("this filesystem refuses non-UTF8 names: %v", err)
	}
	m := complete(t, capture(t, root, st))
	found := false
	for _, en := range m.Entries {
		found = found || en.Path == "" && bytes.HasSuffix(en.RawPath(), []byte("\xe9\xff.txt"))
	}
	if !found {
		t.Fatalf("the odd name was lost: %v", m.Entries)
	}
}

func TestSnapshotLimitsMakeTheManifestIncompleteAndNameThePath(t *testing.T) {
	root, st := fixture(t)
	write(t, root, "big.dat", strings.Repeat("z", 5000))

	m, err := CaptureWith(ctx, root, st, Options{Limits: Limits{MaxPaths: 100, MaxTotalBytes: 1 << 30, MaxFileBytes: 1000}})
	if err != nil {
		t.Fatal(err)
	}
	if m.Complete || !strings.Contains(strings.Join(m.Errors, "\n"), "big.dat") || !strings.Contains(strings.Join(m.Errors, "\n"), "1000") {
		t.Fatalf("per-file limit: complete=%v %v", m.Complete, m.Errors)
	}
	m, _ = CaptureWith(ctx, root, st, Options{Limits: Limits{MaxPaths: 2, MaxTotalBytes: 1 << 30, MaxFileBytes: 1 << 20}})
	if m.Complete || !strings.Contains(strings.Join(m.Errors, "\n"), "2 paths") {
		t.Fatalf("path limit: complete=%v %v", m.Complete, m.Errors)
	}
	m, _ = CaptureWith(ctx, root, st, Options{Limits: Limits{MaxPaths: 100, MaxTotalBytes: 20, MaxFileBytes: 1 << 20}})
	if m.Complete || !strings.Contains(strings.Join(m.Errors, "\n"), "total") {
		t.Fatalf("total limit: complete=%v %v", m.Complete, m.Errors)
	}
	if d := DefaultLimits(); d.MaxPaths != 100000 || d.MaxTotalBytes != 1<<30 || d.MaxFileBytes != 64<<20 {
		t.Fatalf("defaults %+v", d)
	}
}

func TestSnapshotTreatsSubmodulesAndNestedRepositoriesAsBoundaries(t *testing.T) {
	root, st := fixture(t)
	sub := filepath.Join(t.TempDir(), "subrepo")
	os.MkdirAll(sub, 0o755)
	git(t, sub, "init", "-q")
	write(t, sub, "lib.txt", "lib\n")
	git(t, sub, "add", ".")
	git(t, sub, "commit", "-qm", "lib")
	git(t, root, "submodule", "add", "-q", sub, "vendor/lib")
	git(t, root, "commit", "-qm", "add submodule")

	m := complete(t, capture(t, root, st))
	e, ok := entryOf(m, "vendor/lib")
	if !ok || e.Kind != KindGitlink || e.ContentHash == "" {
		t.Fatalf("submodule entry %+v", e)
	}
	if _, inside := entryOf(m, "vendor/lib/lib.txt"); inside {
		t.Fatal("the capture read inside a submodule")
	}

	write(t, root, "vendor/lib/lib.txt", "dirty inside the submodule\n")
	m = capture(t, root, st)
	if m.Complete || !strings.Contains(strings.Join(m.Errors, "\n"), "vendor/lib") {
		t.Fatalf("a dirty submodule cannot be proven task-local: complete=%v %v", m.Complete, m.Errors)
	}

	nested := filepath.Join(root, "nested")
	os.MkdirAll(nested, 0o755)
	git(t, nested, "init", "-q")
	write(t, nested, "x", "x")
	m = capture(t, root, st)
	if m.Complete || !strings.Contains(strings.Join(m.Errors, "\n"), "nested") {
		t.Fatalf("an untracked nested repository: complete=%v %v", m.Complete, m.Errors)
	}
}

func TestSnapshotRefusesWhatIsNotACheckout(t *testing.T) {
	_, st := fixture(t)
	if _, err := Capture(ctx, t.TempDir(), st); err == nil {
		t.Fatal("a directory outside any repository was captured")
	}
	if _, err := Capture(ctx, filepath.Join(t.TempDir(), "absent"), st); err == nil {
		t.Fatal("a missing directory was captured")
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	root, _ := fixture(t)
	if _, err := Capture(cctx, root, st); err == nil {
		t.Fatal("a cancelled capture returned a manifest")
	}
}

func TestDiffRefusesMalformedManifests(t *testing.T) {
	file := func(name string) Entry {
		e := newEntry([]byte(name))
		e.Kind, e.ContentHash = KindFile, sum(name)
		return e
	}
	good := Manifest{Complete: true, Entries: []Entry{file("a"), file("b")}}
	unsorted := Manifest{Complete: true, Entries: []Entry{good.Entries[1], good.Entries[0]}}
	dup := Manifest{Complete: true, Entries: []Entry{good.Entries[0], good.Entries[0]}}
	for name, m := range map[string]Manifest{"unsorted": unsorted, "duplicate": dup} {
		if _, err := Diff(good, m); err == nil {
			t.Errorf("%s manifest accepted", name)
		}
	}
	p, err := Diff(good, good)
	if err != nil || len(p.Changes) != 0 || !p.Complete {
		t.Fatalf("identical manifests: %+v %v", p, err)
	}
}

func TestLoadManifestAndStorePatchRoundTrip(t *testing.T) {
	root, st := fixture(t)
	start := complete(t, capture(t, root, st))
	write(t, root, "src/a.txt", "changed\n")
	end := complete(t, capture(t, root, st))

	loaded, err := LoadManifest(ctx, st, start.Hash)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Hash != start.Hash || !loaded.Complete || len(loaded.Entries) != len(start.Entries) {
		t.Fatalf("loaded %+v", loaded)
	}
	// A loaded manifest diffs exactly like the captured one.
	a, b := diff(t, start, end), diff(t, loaded, end)
	if a.Hash != b.Hash || len(b.Changes) != 1 {
		t.Fatalf("patches differ: %s vs %s", a.Hash, b.Hash)
	}
	h, err := StorePatch(ctx, st, a)
	if err != nil || h != a.Hash {
		t.Fatalf("StorePatch = %s, %v; the artifact must be named by the patch's own hash %s", h, err, a.Hash)
	}
	if raw, err := st.ReadAll(ctx, h, 1<<20); err != nil || !bytes.Contains(raw, []byte(`"src/a.txt"`)) {
		t.Fatalf("%q %v", raw, err)
	}
	if _, err := LoadManifest(ctx, st, sum("not a manifest")); err == nil {
		t.Fatal("a hash that is not stored loaded")
	}
	junk, _ := st.Put(ctx, bytes.NewReader([]byte("{\"version\":99}\n")))
	if _, err := LoadManifest(ctx, st, junk); err == nil {
		t.Fatal("an artifact that is not a manifest loaded")
	}
	// An incomplete patch is not stored: it would read as evidence.
	bad := a
	bad.Complete = false
	if _, err := StorePatch(ctx, st, bad); err == nil {
		t.Fatal("an incomplete patch was stored")
	}
}

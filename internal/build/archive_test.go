package build

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/plan"
)

// histRoot is where tests that replace a build keep its archive, so none of
// them writes next to the shared temp directory.
var histRoot string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "tandem-history-*")
	if err != nil {
		panic(err)
	}
	histRoot = dir
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// evidenceBuild starts a build in <root>/.tandem-relay/build and gives task 1 a
// report, a review report and a check log, all under the relay.
func evidenceBuild(t *testing.T, root, sha, marker string) (dir string, b *Build) {
	t.Helper()
	dir = filepath.Join(root, ".tandem-relay", "build")
	o := opts()
	o.HistoryRoot = root
	b, _, err := Start(dir, plan.Split(planText), "/repo/docs/plans/p.md", sha, o, false)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	reports := filepath.Join(root, ".tandem-relay", "reports")
	if err := os.MkdirAll(filepath.Join(dir, "checks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(reports, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(path, body string) string {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	b.Tasks[0].CheckLog = write(filepath.Join(dir, "checks", "task-1.log"), "check "+marker)
	b.Tasks[0].Report = write(filepath.Join(reports, marker+"-report.md"), "report "+marker)
	b.Tasks[0].ReviewReport = write(filepath.Join(dir, "task-1.review.md"), "review "+marker)
	b.Tasks[0].Status = StatusDone
	if err := b.Save(dir); err != nil {
		t.Fatal(err)
	}
	return dir, b
}

func readArchived(t *testing.T, archive string) *Build {
	t.Helper()
	b, err := Load(filepath.Join(archive, "build"))
	if err != nil || b == nil {
		t.Fatalf("archived build: %v", err)
	}
	return b
}

func bodyOf(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestReplacementArchivesReferencedEvidence(t *testing.T) {
	root := t.TempDir()
	dir, _ := evidenceBuild(t, root, "sha1", "first")
	// A different plan replaces the build.
	o := opts()
	o.HistoryRoot = root
	if _, _, err := Start(dir, plan.Split(planText), "/repo/docs/plans/p.md", "sha2", o, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "checks", "task-1.log")); !os.IsNotExist(err) {
		t.Fatalf("the replaced build's evidence is still in the relay: %v", err)
	}
	b2, _ := Load(dir)
	reports := filepath.Join(root, ".tandem-relay", "reports")
	b2.Tasks[0].Report = filepath.Join(reports, "second-report.md")
	if err := os.WriteFile(b2.Tasks[0].Report, []byte("report second"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := b2.Save(dir); err != nil {
		t.Fatal(err)
	}
	// What tandem-unpair does before it deletes the relay.
	res, err := Archive(root, dir)
	if err != nil || res.Dir == "" {
		t.Fatalf("Archive: %+v %v", res, err)
	}
	if err := os.RemoveAll(filepath.Join(root, ".tandem-relay")); err != nil {
		t.Fatal(err)
	}

	archives, _ := filepath.Glob(filepath.Join(root, HistoryDir, "legacy", "b1-*"))
	if len(archives) != 2 {
		t.Fatalf("want two archives, got %v", archives)
	}
	var sawFirst, sawSecond bool
	for _, a := range archives {
		got := readArchived(t, a)
		report := bodyOf(t, got.Tasks[0].Report)
		switch report {
		case "report first":
			sawFirst = true
			if bodyOf(t, got.Tasks[0].CheckLog) != "check first" || bodyOf(t, got.Tasks[0].ReviewReport) != "review first" {
				t.Errorf("first archive lost its check or review report")
			}
		case "report second":
			sawSecond = true
		}
		if !strings.HasPrefix(got.Tasks[0].Report, a) {
			t.Errorf("report path %q was not rewritten into %s", got.Tasks[0].Report, a)
		}
		if err := VerifyArchive(a); err != nil {
			t.Errorf("archive does not verify: %v", err)
		}
	}
	if !sawFirst || !sawSecond {
		t.Fatalf("first=%v second=%v", sawFirst, sawSecond)
	}
}

func TestArchiveManifestIsSortedAndMatchesTheBytes(t *testing.T) {
	root := t.TempDir()
	dir, _ := evidenceBuild(t, root, "sha1", "m")
	res, err := Archive(root, dir)
	if err != nil {
		t.Fatal(err)
	}
	var m Manifest
	if err := json.Unmarshal([]byte(bodyOf(t, filepath.Join(res.Dir, "manifest.json"))), &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Files) < 4 {
		t.Fatalf("manifest %+v", m)
	}
	for i, f := range m.Files {
		if i > 0 && m.Files[i-1].Path >= f.Path {
			t.Errorf("manifest not sorted at %q", f.Path)
		}
		sum := sha256.Sum256([]byte(bodyOf(t, filepath.Join(res.Dir, f.Path))))
		if hex.EncodeToString(sum[:]) != f.SHA256 {
			t.Errorf("%s: manifest hash does not match the bytes", f.Path)
		}
	}
	if gi := bodyOf(t, filepath.Join(root, HistoryDir, ".gitignore")); !strings.Contains(gi, "*") {
		t.Errorf("history directory does not ignore itself: %q", gi)
	}
}

func TestArchiveFailurePreservesSource(t *testing.T) {
	root := t.TempDir()
	dir, _ := evidenceBuild(t, root, "sha1", "keep")
	before := bodyOf(t, filepath.Join(dir, "build.json"))

	orig := writeArchiveFile
	writeArchiveFile = func(path string, data []byte) error {
		// A short write: half the bytes reach the disk and nothing reports it.
		return os.WriteFile(path, data[:len(data)/2], 0o644)
	}
	t.Cleanup(func() { writeArchiveFile = orig })

	if _, err := Archive(root, dir); err == nil {
		t.Fatal("a short write was archived")
	}
	o := opts()
	o.HistoryRoot = root
	if _, _, err := Start(dir, plan.Split(planText), "/repo/docs/plans/p.md", "sha2", o, false); err == nil {
		t.Fatal("replacement went ahead although the archive failed")
	}
	if bodyOf(t, filepath.Join(dir, "build.json")) != before || bodyOf(t, filepath.Join(dir, "checks", "task-1.log")) != "check keep" {
		t.Fatal("the source changed")
	}
	if left, _ := filepath.Glob(filepath.Join(root, HistoryDir, "legacy", "*")); len(left) != 0 {
		t.Fatalf("a half-written archive was left behind: %v", left)
	}
}

func TestArchiveRejectsEscapingReport(t *testing.T) {
	root := t.TempDir()
	dir, b := evidenceBuild(t, root, "sha1", "esc")
	outside := filepath.Join(t.TempDir(), "secret.md")
	if err := os.WriteFile(outside, []byte("not the build's to copy"), 0o644); err != nil {
		t.Fatal(err)
	}
	b.Tasks[0].Report = outside
	if err := b.Save(dir); err != nil {
		t.Fatal(err)
	}
	_, err := Archive(root, dir)
	if !errors.Is(err, ErrEscapes) {
		t.Fatalf("err = %v, want ErrEscapes", err)
	}
	found := false
	filepath.Walk(filepath.Join(root, HistoryDir), func(p string, info os.FileInfo, _ error) error {
		if info != nil && !info.IsDir() && strings.Contains(bodyOf(t, p), "not the build's to copy") {
			found = true
		}
		return nil
	})
	if found {
		t.Fatal("a file outside the relay was copied")
	}
}

func TestArchiveRejectsASymlinkedReport(t *testing.T) {
	root := t.TempDir()
	dir, b := evidenceBuild(t, root, "sha1", "link")
	target := filepath.Join(t.TempDir(), "elsewhere.md")
	if err := os.WriteFile(target, []byte("elsewhere"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, ".tandem-relay", "reports", "linked.md")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	b.Tasks[0].Report = link
	if err := b.Save(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := Archive(root, dir); !errors.Is(err, ErrEscapes) {
		t.Fatalf("err = %v, want ErrEscapes", err)
	}
}

func TestArchiveRecordsAMissingReferencedFile(t *testing.T) {
	root := t.TempDir()
	dir, b := evidenceBuild(t, root, "sha1", "gone")
	if err := os.Remove(b.Tasks[0].Report); err != nil {
		t.Fatal(err)
	}
	res, err := Archive(root, dir)
	if err != nil {
		t.Fatalf("a report that was already gone must not block archiving: %v", err)
	}
	var m Manifest
	if err := json.Unmarshal([]byte(bodyOf(t, filepath.Join(res.Dir, "manifest.json"))), &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Missing) != 1 {
		t.Fatalf("missing = %v", m.Missing)
	}
}

func TestArchiveOfNoBuildIsANoOp(t *testing.T) {
	root := t.TempDir()
	res, err := Archive(root, filepath.Join(root, ".tandem-relay", "build"))
	if err != nil || res.Dir != "" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if _, err := os.Stat(filepath.Join(root, HistoryDir)); !os.IsNotExist(err) {
		t.Fatal("created history for nothing")
	}
}

func TestStartWithoutAHistoryRootRefusesToDeleteABuild(t *testing.T) {
	root := t.TempDir()
	dir, _ := evidenceBuild(t, root, "sha1", "nohist")
	o := opts()
	o.HistoryRoot = ""
	if _, _, err := Start(dir, plan.Split(planText), "/repo/docs/plans/p.md", "sha2", o, false); err == nil {
		t.Fatal("replaced a build with nowhere to keep its history")
	}
	if _, err := os.Stat(filepath.Join(dir, "build.json")); err != nil {
		t.Fatal("the build was removed")
	}
}

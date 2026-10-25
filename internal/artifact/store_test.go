package artifact

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/emsnoel/tandem/internal/workflow"
)

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func newStore(t *testing.T) (*Store, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "state")
	s, err := Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s, root
}

func put(t *testing.T, s *Store, data []byte) string {
	t.Helper()
	h, err := s.Put(context.Background(), bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	return h
}

func published(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	filepath.Walk(filepath.Join(root, "artifacts", "sha256"), func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			out = append(out, p)
		}
		return nil
	})
	return out
}

func TestPutStoresBytesUnderTheirHash(t *testing.T) {
	s, root := newStore(t)
	data := []byte("hello artifact\n")
	h := put(t, s, data)
	if h != sum(data) {
		t.Fatalf("hash %s, want %s", h, sum(data))
	}
	want := filepath.Join(root, "artifacts", "sha256", h[:2], h)
	got, err := os.ReadFile(want)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("stored bytes: %q, %v", got, err)
	}
	info, _ := os.Stat(want)
	if info.Mode().Perm() != 0o400 {
		t.Errorf("artifact mode %v, want 0400", info.Mode().Perm())
	}
	if d, _ := os.Stat(filepath.Dir(want)); d.Mode().Perm() != 0o700 {
		t.Errorf("shard directory mode %v, want 0700", d.Mode().Perm())
	}
}

func TestPutTwiceReusesTheObject(t *testing.T) {
	s, root := newStore(t)
	a := put(t, s, []byte("same"))
	before, _ := os.Stat(filepath.Join(root, "artifacts", "sha256", a[:2], a))
	b := put(t, s, []byte("same"))
	after, _ := os.Stat(filepath.Join(root, "artifacts", "sha256", a[:2], a))
	if a != b || !os.SameFile(before, after) {
		t.Fatal("duplicate bytes were stored twice")
	}
	if n := len(published(t, root)); n != 1 {
		t.Fatalf("%d published objects", n)
	}
	if left, _ := os.ReadDir(filepath.Join(root, "artifacts", "tmp")); len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}
}

func TestPutEmptyArtifact(t *testing.T) {
	s, _ := newStore(t)
	h := put(t, s, nil)
	if h != sum(nil) {
		t.Fatalf("hash %s", h)
	}
	c, err := s.Read(context.Background(), h, 0, 0)
	if err != nil || len(c.Data) != 0 || c.NextOffset != nil || c.TotalBytes != 0 {
		t.Fatalf("chunk %+v err %v", c, err)
	}
}

// Nothing may be published under a hash until every byte is on disk, so a
// crash before the rename must leave only a temp file outside the namespace.
func TestArtifactPutInterruptedLeavesNoPublishedObject(t *testing.T) {
	s, root := newStore(t)
	data := []byte("never finished")
	s.hooks.beforeRename = func() error { return errCrash }
	_, err := s.Put(context.Background(), bytes.NewReader(data))
	if !errors.Is(err, errCrash) {
		t.Fatalf("err = %v", err)
	}
	if got := published(t, root); len(got) != 0 {
		t.Fatalf("a partial object was published: %v", got)
	}
	if _, err := s.Read(context.Background(), sum(data), 0, 0); err == nil {
		t.Fatal("the hash of an unfinished write can be read")
	}
	tmp, _ := os.ReadDir(filepath.Join(root, "artifacts", "tmp"))
	if len(tmp) != 1 {
		t.Fatalf("want the one removable temp file, got %v", tmp)
	}
	// The crash leaves the temp behind (a real crash cannot clean up); a later
	// put of the same bytes still works and publishes a valid object.
	s.hooks.beforeRename = nil
	if h := put(t, s, data); h != sum(data) {
		t.Fatalf("retry hash %s", h)
	}
}

func TestPutCleansUpAfterAnOrdinaryFailure(t *testing.T) {
	s, root := newStore(t)
	boom := errors.New("disk full")
	_, err := s.Put(context.Background(), io.MultiReader(strings.NewReader("part"), errReader{boom}))
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if tmp, _ := os.ReadDir(filepath.Join(root, "artifacts", "tmp")); len(tmp) != 0 {
		t.Fatalf("a failed put left %v", tmp)
	}
	if got := published(t, root); len(got) != 0 {
		t.Fatalf("published %v", got)
	}
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

func TestPutHonoursCancellation(t *testing.T) {
	s, root := newStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Put(ctx, bytes.NewReader(bytes.Repeat([]byte("x"), 1<<20))); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if got := published(t, root); len(got) != 0 {
		t.Fatalf("published %v", got)
	}
}

func TestArtifactReadBoundsAndEOF(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	data := bytes.Repeat([]byte("0123456789"), 5000) // 50,000 bytes
	h := put(t, s, data)

	c, err := s.Read(ctx, h, 0, 0) // limit 0 means the default, 32768
	if err != nil || len(c.Data) != DefaultLimit || c.Offset != 0 || c.TotalBytes != 50000 || c.Hash != h {
		t.Fatalf("default read: len %d offset %d total %d err %v", len(c.Data), c.Offset, c.TotalBytes, err)
	}
	if c.NextOffset == nil || *c.NextOffset != DefaultLimit {
		t.Fatalf("next offset %v", c.NextOffset)
	}
	c, err = s.Read(ctx, h, DefaultLimit, 0)
	if err != nil || len(c.Data) != 50000-DefaultLimit || c.NextOffset != nil {
		t.Fatalf("last chunk: len %d next %v err %v", len(c.Data), c.NextOffset, err)
	}
	if !bytes.Equal(c.Data, data[DefaultLimit:]) {
		t.Fatal("last chunk bytes differ")
	}
	// Exactly at the end: success, no data, no next offset.
	c, err = s.Read(ctx, h, 50000, 10)
	if err != nil || len(c.Data) != 0 || c.NextOffset != nil || c.Offset != 50000 {
		t.Fatalf("read at EOF: %+v err %v", c, err)
	}
	for name, args := range map[string][2]int64{
		"beyond the end":  {50001, 10},
		"negative offset": {-1, 10},
		"negative limit":  {0, -1},
		"limit too large": {0, 32769},
	} {
		if _, err := s.Read(ctx, h, args[0], int(args[1])); !errors.Is(err, ErrInvalidRange) {
			t.Errorf("%s: err = %v, want ErrInvalidRange", name, err)
		}
	}
	c, err = s.Read(ctx, h, 5, 1) // smallest permitted limit
	if err != nil || string(c.Data) != "5" || c.NextOffset == nil || *c.NextOffset != 6 {
		t.Fatalf("limit 1: %+v err %v", c, err)
	}
	c, err = s.Read(ctx, h, 0, MaxLimit)
	if err != nil || len(c.Data) != MaxLimit {
		t.Fatalf("limit %d: len %d err %v", MaxLimit, len(c.Data), err)
	}
}

func TestReadReassemblesALargeArtifactInChunks(t *testing.T) {
	s, _ := newStore(t)
	data := make([]byte, 3*MaxLimit+17)
	for i := range data {
		data[i] = byte(i * 7)
	}
	h := put(t, s, data)
	var got []byte
	var off int64
	for steps := 0; ; steps++ {
		c, err := s.Read(context.Background(), h, off, 0)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, c.Data...)
		if c.NextOffset == nil {
			break
		}
		off = *c.NextOffset
		if steps > 10 {
			t.Fatal("too many chunks")
		}
	}
	if !bytes.Equal(got, data) {
		t.Fatal("reassembled bytes differ")
	}
}

// The whole object is verified, not only the range asked for.
func TestArtifactTamperDetectedBeforeChunkReturn(t *testing.T) {
	s, root := newStore(t)
	data := bytes.Repeat([]byte("a"), 40000)
	h := put(t, s, data)
	path := filepath.Join(root, "artifacts", "sha256", h[:2], h)
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteAt([]byte("Z"), 39999) // a byte far outside the range read below
	f.Close()
	c, err := s.Read(context.Background(), h, 0, 10)
	if !errors.Is(err, workflow.ErrCorruptArtifact) {
		t.Fatalf("err = %v, want ErrCorruptArtifact", err)
	}
	if len(c.Data) != 0 {
		t.Fatal("bytes of a corrupt artifact were returned")
	}
}

func TestReadRejectsATruncatedAndASymlinkedObject(t *testing.T) {
	s, root := newStore(t)
	h := put(t, s, []byte("0123456789"))
	path := filepath.Join(root, "artifacts", "sha256", h[:2], h)
	os.Chmod(path, 0o600)
	os.Truncate(path, 4)
	if _, err := s.Read(context.Background(), h, 0, 0); !errors.Is(err, workflow.ErrCorruptArtifact) {
		t.Fatalf("truncated: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "elsewhere")
	os.WriteFile(outside, []byte("0123456789"), 0o600)
	os.Remove(path)
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read(context.Background(), h, 0, 0); !errors.Is(err, workflow.ErrCorruptArtifact) {
		t.Fatalf("symlink: %v", err)
	}
}

func TestPutRefusesToReuseAnObjectThatIsCorrupt(t *testing.T) {
	s, root := newStore(t)
	h := put(t, s, []byte("original bytes"))
	path := filepath.Join(root, "artifacts", "sha256", h[:2], h)
	os.Chmod(path, 0o600)
	os.WriteFile(path, []byte("different bytes"), 0o600)
	if _, err := s.Put(context.Background(), strings.NewReader("original bytes")); !errors.Is(err, workflow.ErrCorruptArtifact) {
		t.Fatalf("err = %v, want ErrCorruptArtifact", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "different bytes" {
		t.Fatal("Put overwrote the corrupt object instead of reporting it")
	}
}

func TestHashesThatAreNotHashesNeverTouchThePath(t *testing.T) {
	s, root := newStore(t)
	secret := filepath.Join(filepath.Dir(root), "secret")
	os.WriteFile(secret, []byte("secret"), 0o600)
	for _, bad := range []string{
		"", "abc", "../../secret", strings.Repeat("A", 64), strings.Repeat("g", 64),
		strings.Repeat("a", 63), strings.Repeat("a", 65), "../" + strings.Repeat("a", 61),
		strings.Repeat("a", 62) + "/.", "..\\..\\x",
	} {
		if _, err := s.Read(context.Background(), bad, 0, 0); !errors.Is(err, ErrInvalidHash) {
			t.Errorf("Read(%q): err = %v, want ErrInvalidHash", bad, err)
		}
	}
	if _, err := s.Read(context.Background(), strings.Repeat("a", 64), 0, 0); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("an absent hash: %v", err)
	}
}

func TestConcurrentPutsOfTheSameBytes(t *testing.T) {
	s, root := newStore(t)
	data := bytes.Repeat([]byte("race"), 100000)
	var wg sync.WaitGroup
	hashes := make(chan string, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h, err := s.Put(context.Background(), bytes.NewReader(data))
			if err != nil {
				t.Error(err)
			}
			hashes <- h
		}()
	}
	wg.Wait()
	close(hashes)
	for h := range hashes {
		if h != sum(data) {
			t.Fatalf("hash %s", h)
		}
	}
	if n := len(published(t, root)); n != 1 {
		t.Fatalf("%d objects", n)
	}
	if _, err := s.Read(context.Background(), sum(data), 0, 1); err != nil {
		t.Fatalf("the stored object does not verify: %v", err)
	}
}

func TestChunkCarriesAMediaTypeFromItsResolver(t *testing.T) {
	s, _ := newStore(t)
	h := put(t, s, []byte("x"))
	c, _ := s.Read(context.Background(), h, 0, 0)
	if c.MediaType != "application/octet-stream" {
		t.Fatalf("default media type %q", c.MediaType)
	}
	s.MediaTypes = func(string) string { return "text/markdown" }
	if c, _ = s.Read(context.Background(), h, 0, 0); c.MediaType != "text/markdown" {
		t.Fatalf("resolved media type %q", c.MediaType)
	}
}

func TestReadAllVerifiesTheWholeObjectAndBoundsItsSize(t *testing.T) {
	s, root := newStore(t)
	data := bytes.Repeat([]byte("abc"), 30000)
	h := put(t, s, data)
	got, err := s.ReadAll(context.Background(), h, int64(len(data)))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("ReadAll: %v len %d", err, len(got))
	}
	if _, err := s.ReadAll(context.Background(), h, 10); !errors.Is(err, ErrInvalidRange) {
		t.Fatalf("an object larger than the limit: %v", err)
	}
	path := filepath.Join(root, "artifacts", "sha256", h[:2], h)
	os.Chmod(path, 0o600)
	os.WriteFile(path, bytes.Repeat([]byte("abd"), 30000), 0o600)
	if _, err := s.ReadAll(context.Background(), h, int64(len(data))); !errors.Is(err, workflow.ErrCorruptArtifact) {
		t.Fatalf("corrupt object: %v", err)
	}
}

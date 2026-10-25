// Package artifact stores immutable, content-addressed bytes on disk, outside
// the relay and outside the repository. A name under artifacts/sha256 denotes
// complete bytes whose hash is that name: content is written to a private
// temporary file, synced, and only then renamed into place.
//
// The package knows nothing about the database. Callers that record an
// artifact's metadata hold the state shared publication lock from Put until
// their references commit, so maintenance cannot collect it in between.
package artifact

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"

	"github.com/emsnoel/tandem/internal/workflow"
)

const (
	// DefaultLimit is the chunk size Read uses when limit is 0.
	DefaultLimit = 32768
	// MaxLimit is the largest chunk Read returns.
	MaxLimit = 32768

	defaultMediaType = "application/octet-stream"
)

var (
	// ErrInvalidHash: the argument is not 64 lowercase hex characters. It is
	// rejected before it can reach a filesystem path.
	ErrInvalidHash = errors.New("invalid artifact hash")
	// ErrInvalidRange: offset or limit is outside what Read allows.
	ErrInvalidRange = errors.New("invalid artifact range")

	hashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// errCrash makes a test hook stop Put as a killed process would: the temporary
// file is left behind. Production code never returns it.
var errCrash = errors.New("simulated crash")

type hooks struct {
	// beforeRename runs after the content is synced and before it is published.
	beforeRename func() error
}

// Chunk is one bounded, verified slice of an artifact.
type Chunk struct {
	Hash       string `json:"hash"`
	MediaType  string `json:"media_type"`
	Data       []byte `json:"data"`
	Offset     int64  `json:"offset"`
	NextOffset *int64 `json:"next_offset"`
	TotalBytes int64  `json:"total_bytes"`
}

// Store is the artifact directory of one checkout's state root.
type Store struct {
	objects string // <root>/artifacts/sha256
	tmp     string // <root>/artifacts/tmp, outside the hash namespace
	hooks   hooks

	// MediaTypes names an artifact's media type for Read. The type belongs to
	// the database row, so the state layer supplies it; the default for an
	// unknown artifact is application/octet-stream.
	MediaTypes func(hash string) string
}

// Open prepares the artifact directories under root, creating them private.
func Open(root string) (*Store, error) {
	s := &Store{
		objects: filepath.Join(root, "artifacts", "sha256"),
		tmp:     filepath.Join(root, "artifacts", "tmp"),
	}
	for _, dir := range []string{s.objects, s.tmp} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *Store) path(hash string) string { return filepath.Join(s.objects, hash[:2], hash) }

// Put streams r into the store and returns the sha256 of its bytes.
//
//	temp_write -> file_sync -> atomic_rename -> directory_sync -> return_hash
//
// Bytes already stored under that hash are verified and reused; stored bytes
// that do not match their name are ErrCorruptArtifact, never overwritten.
func (s *Store) Put(ctx context.Context, r io.Reader) (string, error) {
	f, tmpPath, err := s.createTemp()
	if err != nil {
		return "", err
	}
	abandon := func(err error) (string, error) {
		f.Close()
		if !errors.Is(err, errCrash) {
			os.Remove(tmpPath)
		}
		return "", err
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), &ctxReader{ctx, r}); err != nil {
		return abandon(err)
	}
	if err := f.Sync(); err != nil {
		return abandon(err)
	}
	if err := f.Close(); err != nil {
		return abandon(err)
	}
	hash := hex.EncodeToString(h.Sum(nil))
	if s.hooks.beforeRename != nil {
		if err := s.hooks.beforeRename(); err != nil {
			return abandon(err)
		}
	}

	dest := s.path(hash)
	if _, err := os.Lstat(dest); err == nil {
		os.Remove(tmpPath)
		if err := verify(ctx, dest, hash); err != nil {
			return "", err
		}
		return hash, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		os.Remove(tmpPath)
		return "", err
	}
	shard := filepath.Dir(dest)
	if err := os.MkdirAll(shard, 0o700); err != nil {
		os.Remove(tmpPath)
		return "", err
	}
	if err := os.Chmod(tmpPath, 0o400); err != nil {
		os.Remove(tmpPath)
		return "", err
	}
	if err := os.Rename(tmpPath, dest); err != nil {
		os.Remove(tmpPath)
		return "", err
	}
	// Make the rename and any new shard directory durable before the hash is
	// handed to a caller that will record it.
	for _, dir := range []string{shard, s.objects} {
		if err := syncDir(dir); err != nil {
			return "", err
		}
	}
	return hash, nil
}

func (s *Store) createTemp() (*os.File, string, error) {
	for attempt := 0; attempt < 8; attempt++ {
		suffix := make([]byte, 16)
		if _, err := rand.Read(suffix); err != nil {
			return nil, "", err
		}
		p := filepath.Join(s.tmp, "put-"+hex.EncodeToString(suffix))
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		return f, p, err
	}
	return nil, "", errors.New("could not create a unique temporary file")
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// Read returns a bounded slice of an artifact. The whole object is streamed
// through the hash first, and no byte is returned unless it matches the name.
//
// limit 0 means DefaultLimit; otherwise it must be 1..MaxLimit. offset must be
// 0..size. Reading at exactly the end succeeds with no data and a nil
// NextOffset; beyond it is ErrInvalidRange.
func (s *Store) Read(ctx context.Context, hash string, offset int64, limit int) (Chunk, error) {
	if !hashPattern.MatchString(hash) {
		return Chunk{}, fmt.Errorf("%w: %q", ErrInvalidHash, hash)
	}
	if limit == 0 {
		limit = DefaultLimit
	}
	if limit < 0 || limit > MaxLimit {
		return Chunk{}, fmt.Errorf("%w: limit %d is outside 1..%d", ErrInvalidRange, limit, MaxLimit)
	}
	if offset < 0 {
		return Chunk{}, fmt.Errorf("%w: negative offset %d", ErrInvalidRange, offset)
	}
	path := s.path(hash)
	f, size, err := openRegular(path, hash)
	if err != nil {
		return Chunk{}, err
	}
	defer f.Close()
	if offset > size {
		return Chunk{}, fmt.Errorf("%w: offset %d is beyond the %d byte artifact", ErrInvalidRange, offset, size)
	}

	want := int64(limit)
	if offset+want > size {
		want = size - offset
	}
	data := make([]byte, 0, want)
	h := sha256.New()
	var pos int64
	buf := make([]byte, 32*1024)
	for {
		if err := ctx.Err(); err != nil {
			return Chunk{}, err
		}
		n, rerr := f.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
			if end := pos + int64(n); end > offset && pos < offset+want {
				lo, hi := max(offset-pos, 0), min(offset+want-pos, int64(n))
				data = append(data, buf[lo:hi]...)
			}
			pos += int64(n)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return Chunk{}, rerr
		}
	}
	if pos != size || hex.EncodeToString(h.Sum(nil)) != hash {
		return Chunk{}, fmt.Errorf("%w: %s read %d bytes and does not match its hash", workflow.ErrCorruptArtifact, hash, pos)
	}
	c := Chunk{Hash: hash, MediaType: defaultMediaType, Data: data, Offset: offset, TotalBytes: size}
	if s.MediaTypes != nil {
		if mt := s.MediaTypes(hash); mt != "" {
			c.MediaType = mt
		}
	}
	if next := offset + int64(len(data)); next < size {
		c.NextOffset = &next
	}
	return c, nil
}

// openRegular opens path, which must be a regular file and not a symlink.
func openRegular(path, hash string) (*os.File, int64, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, 0, err
	}
	if !info.Mode().IsRegular() {
		return nil, 0, fmt.Errorf("%w: %s is not a regular file", workflow.ErrCorruptArtifact, hash)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	return f, info.Size(), nil
}

// verify streams the stored file through the hash and compares it with its name.
func verify(ctx context.Context, path, hash string) error {
	f, _, err := openRegular(path, hash)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, &ctxReader{ctx, f}); err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != hash {
		return fmt.Errorf("%w: stored bytes for %s do not match their hash", workflow.ErrCorruptArtifact, hash)
	}
	return nil
}

// ctxReader stops a copy when its context ends.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// ReadAll returns an artifact's whole content, verified against its hash. It
// is for artifacts known to be small (manifests, patches); an object larger
// than limit is ErrInvalidRange and nothing is read into memory.
func (s *Store) ReadAll(ctx context.Context, hash string, limit int64) ([]byte, error) {
	if !hashPattern.MatchString(hash) {
		return nil, fmt.Errorf("%w: %q", ErrInvalidHash, hash)
	}
	f, size, err := openRegular(s.path(hash), hash)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if size > limit {
		return nil, fmt.Errorf("%w: %s is %d bytes, over the limit of %d", ErrInvalidRange, hash, size, limit)
	}
	data := make([]byte, 0, size)
	h := sha256.New()
	buf := make([]byte, 32*1024)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n, rerr := f.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
			data = append(data, buf[:n]...)
			if int64(len(data)) > limit {
				return nil, fmt.Errorf("%w: %s grew past the limit of %d while being read", ErrInvalidRange, hash, limit)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return nil, rerr
		}
	}
	if int64(len(data)) != size || hex.EncodeToString(h.Sum(nil)) != hash {
		return nil, fmt.Errorf("%w: %s read %d bytes and does not match its hash", workflow.ErrCorruptArtifact, hash, len(data))
	}
	return data, nil
}

// ScratchDir is a private directory on the same filesystem as the objects and
// outside the repository and the relay, for files that are written first and
// imported later, such as a check's log. Nothing in it is an artifact.
func (s *Store) ScratchDir() string { return s.tmp }

//go:build darwin || linux

package snapshot

// Capture is built on O_NOFOLLOW and inode identity, which this package
// supports on macOS and Linux, as does internal/state.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/emsnoel/tandem/internal/artifact"
	"github.com/emsnoel/tandem/internal/paths"
)

// Limits bound a capture. They are configuration, not permission to truncate:
// exceeding one makes the manifest incomplete and names the path and the limit.
type Limits struct {
	MaxPaths      int
	MaxTotalBytes int64
	MaxFileBytes  int64
}

// DefaultLimits are 100,000 paths, 1 GiB in total and 64 MiB per file.
func DefaultLimits() Limits {
	return Limits{MaxPaths: 100000, MaxTotalBytes: 1 << 30, MaxFileBytes: 64 << 20}
}

// Options configure CaptureWith.
type Options struct {
	Limits Limits // zero means DefaultLimits
	// afterRead is a test hook: it runs after a file's bytes were read and
	// before the stability check, where a concurrent writer would land.
	afterRead func(path string, attempt int)
}

// Capture snapshots the checkout at root with the default limits, storing file
// contents and the manifest in artifacts. It never writes to the repository or
// its index. The error is for failures that make a snapshot meaningless (not a
// repository, cancelled); anything narrower yields Complete=false.
func Capture(ctx context.Context, root string, artifacts *artifact.Store) (Manifest, error) {
	return CaptureWith(ctx, root, artifacts, Options{})
}

type listed struct {
	raw    []byte
	gitSHA string // index object for a tracked path; "" for an untracked one
	mode   string // git mode of a tracked path
	nested bool   // an untracked directory that is itself a repository
}

// CaptureWith is Capture with explicit limits and test hooks.
func CaptureWith(ctx context.Context, root string, artifacts *artifact.Store, opts Options) (Manifest, error) {
	limits := opts.Limits
	if limits == (Limits{}) {
		limits = DefaultLimits()
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return Manifest{}, fmt.Errorf("%s is not a directory: %v", root, err)
	}
	files, err := list(ctx, root)
	if err != nil {
		return Manifest{}, err
	}

	c := &capturer{ctx: ctx, root: root, store: artifacts, limits: limits, hook: opts.afterRead}
	if len(files) > limits.MaxPaths {
		c.errorf("the checkout lists %d paths, over the limit of %d paths; only the first %d (by path) were considered", len(files), limits.MaxPaths, limits.MaxPaths)
		files = files[:limits.MaxPaths]
	}
	var unstable []listed
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return Manifest{}, err
		}
		switch {
		case f.nested:
			c.errorf("nested repository %s is not captured: it is neither tracked nor ignored", string(f.raw))
		case f.mode == "160000":
			c.gitlink(f)
		default:
			if ok := c.file(f, 1); !ok {
				unstable = append(unstable, f)
			}
		}
	}
	// One retry for files that changed while they were being read.
	for _, f := range unstable {
		if err := ctx.Err(); err != nil {
			return Manifest{}, err
		}
		if ok := c.file(f, 2); !ok {
			c.errorf("%s changed while it was being read, twice; it was not captured", string(f.raw))
		}
	}

	sortEntries(c.entries)
	sort.Strings(c.errs)
	m := Manifest{Complete: len(c.errs) == 0, Errors: c.errs, Entries: c.entries}
	if m.Errors == nil {
		m.Errors = []string{}
	}
	body, _, err := canonical(manifestDoc{Version: 1, Complete: m.Complete, Errors: m.Errors, Entries: m.Entries})
	if err != nil {
		return Manifest{}, err
	}
	if m.Hash, err = artifacts.Put(ctx, bytes.NewReader(body)); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

type capturer struct {
	ctx     context.Context
	root    string
	store   *artifact.Store
	limits  Limits
	hook    func(path string, attempt int)
	entries []Entry
	errs    []string
	total   int64
	full    bool // the total-bytes limit was hit; no further file is read
}

func (c *capturer) errorf(format string, a ...any) {
	c.errs = append(c.errs, fmt.Sprintf(format, a...))
}

// list enumerates tracked paths (with their mode and index object) and
// untracked, non-ignored paths, NUL delimited and without touching the index.
func list(ctx context.Context, root string) ([]listed, error) {
	run := func(args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("git %s in %s: %w", strings.Join(args, " "), root, err)
		}
		return out, nil
	}
	tracked, err := run("ls-files", "-z", "-s")
	if err != nil {
		return nil, err
	}
	seen := map[string]listed{}
	for _, rec := range bytes.Split(tracked, []byte{0}) {
		if len(rec) == 0 {
			continue
		}
		meta, path, ok := bytes.Cut(rec, []byte{'\t'})
		fields := strings.Fields(string(meta))
		if !ok || len(fields) != 3 {
			return nil, fmt.Errorf("unexpected git ls-files record %q", rec)
		}
		if skip(path) {
			continue
		}
		seen[string(path)] = listed{raw: path, mode: fields[0], gitSHA: fields[1]}
	}
	others, err := run("ls-files", "-z", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	for _, path := range bytes.Split(others, []byte{0}) {
		if len(path) == 0 || skip(path) {
			continue
		}
		if path[len(path)-1] == '/' {
			seen[string(path)] = listed{raw: bytes.TrimSuffix(path, []byte{'/'}), nested: true}
			continue
		}
		if _, tracked := seen[string(path)]; !tracked {
			seen[string(path)] = listed{raw: path}
		}
	}
	out := make([]listed, 0, len(seen))
	for _, l := range seen {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return bytes.Compare(out[i].raw, out[j].raw) < 0 })
	return out, nil
}

// skip drops transport and history directories: they are never part of a task.
func skip(path []byte) bool {
	for _, part := range strings.Split(string(path), "/") {
		if paths.IsRelayName(part) || part == ".tandem-history" {
			return true
		}
	}
	return false
}

// file captures one path. It returns false only when the file changed while it
// was read, so the caller may retry; every other outcome is final.
func (c *capturer) file(f listed, attempt int) (stable bool) {
	if c.full {
		return true
	}
	path := filepath.Join(c.root, string(f.raw))
	before, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return true // tracked but absent: nothing to record
	}
	if err != nil {
		c.errorf("%s: %v", string(f.raw), err)
		return true
	}
	entry := newEntry(f.raw)

	switch mode := before.Mode(); {
	case mode&fs.ModeSymlink != 0:
		target, err := os.Readlink(path)
		if err != nil {
			c.errorf("%s: reading the link: %v", string(f.raw), err)
			return true
		}
		// The target text is the content. Where it points is never followed.
		hash, err := c.store.Put(c.ctx, strings.NewReader(target))
		if err != nil {
			c.errorf("%s: storing the link target: %v", string(f.raw), err)
			return true
		}
		entry.Kind, entry.ContentHash, entry.Size = KindSymlink, hash, int64(len(target))
	case mode.IsRegular():
		if before.Size() > c.limits.MaxFileBytes {
			c.errorf("%s is %d bytes, over the per-file limit of %d bytes", string(f.raw), before.Size(), c.limits.MaxFileBytes)
			return true
		}
		if c.total+before.Size() > c.limits.MaxTotalBytes {
			c.errorf("capturing %s would pass the total limit of %d bytes; the rest of the checkout was not captured", string(f.raw), c.limits.MaxTotalBytes)
			c.full = true
			return true
		}
		hash, read, err := c.read(path, before)
		if err != nil {
			c.errorf("%s: %v", string(f.raw), err)
			return true
		}
		if c.hook != nil {
			c.hook(string(f.raw), attempt)
		}
		after, err := os.Lstat(path)
		if err != nil || !sameFile(before, after) || read != before.Size() {
			return false
		}
		entry.Kind, entry.Executable, entry.ContentHash, entry.Size = KindFile, mode&0o100 != 0, hash, read
		c.total += read
	default:
		c.errorf("%s is not a regular file, a symlink or a submodule (mode %v)", string(f.raw), mode)
		return true
	}
	c.entries = append(c.entries, entry)
	return true
}

// read streams a regular file into the artifact store without following a
// symlink swapped in after the Lstat.
func (c *capturer) read(path string, before fs.FileInfo) (hash string, n int64, err error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return "", 0, err
	}
	if !os.SameFile(before, opened) {
		return "", 0, errors.New("the path was replaced while it was being opened")
	}
	r := &countingReader{r: io.LimitReader(f, c.limits.MaxFileBytes+1)}
	hash, err = c.store.Put(c.ctx, r)
	if err != nil {
		return "", 0, err
	}
	if r.n > c.limits.MaxFileBytes {
		return "", 0, fmt.Errorf("grew past the per-file limit of %d bytes while being read", c.limits.MaxFileBytes)
	}
	return hash, r.n, nil
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// sameFile reports whether two Lstat results describe the same unchanged file.
func sameFile(a, b fs.FileInfo) bool {
	return os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime()) && a.Mode() == b.Mode()
}

// gitlink records the commit a submodule's working tree is at. Anything
// uncommitted inside it is invisible to this capture, so a dirty submodule
// makes the manifest incomplete rather than silently clean.
func (c *capturer) gitlink(f listed) {
	dir := filepath.Join(c.root, string(f.raw))
	commit := f.gitSHA
	if out, err := exec.CommandContext(c.ctx, "git", "-C", dir, "rev-parse", "--show-toplevel").Output(); err == nil {
		if top, _ := filepath.EvalSymlinks(strings.TrimSpace(string(out))); top == mustReal(dir) {
			if head, err := exec.CommandContext(c.ctx, "git", "-C", dir, "rev-parse", "HEAD").Output(); err == nil {
				commit = strings.TrimSpace(string(head))
			}
			st := exec.CommandContext(c.ctx, "git", "-C", dir, "status", "--porcelain", "--ignore-submodules=none")
			st.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
			if out, err := st.Output(); err != nil {
				c.errorf("%s: could not inspect the submodule: %v", string(f.raw), err)
				return
			} else if len(bytes.TrimSpace(out)) != 0 {
				c.errorf("submodule %s has uncommitted changes that this capture cannot see", string(f.raw))
				return
			}
		}
	}
	body := "gitlink " + commit + "\n"
	hash, err := c.store.Put(c.ctx, strings.NewReader(body))
	if err != nil {
		c.errorf("%s: %v", string(f.raw), err)
		return
	}
	entry := newEntry(f.raw)
	entry.Kind, entry.ContentHash, entry.Size = KindGitlink, hash, int64(len(body))
	c.entries = append(c.entries, entry)
}

func mustReal(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

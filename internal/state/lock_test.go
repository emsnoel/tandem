package state

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/emsnoel/tandem/internal/artifact"
	"github.com/emsnoel/tandem/internal/workflow"
)

func newLocker(t *testing.T) (*Locker, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "state")
	l, err := OpenLocker(root)
	if err != nil {
		t.Fatal(err)
	}
	return l, root
}

// deadline bounds every wait in these tests: a lock that wrongly blocks must
// fail the test, not hang the suite.
func deadline(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestSharedLocksCoexistAndExcludeMaintenance(t *testing.T) {
	l, root := newLocker(t)
	ctx := deadline(t)
	a, err := l.LockShared(ctx)
	if err != nil {
		t.Fatal(err)
	}
	other, _ := OpenLocker(root)
	b, err := other.LockShared(ctx)
	if err != nil {
		t.Fatalf("two publishers must not block each other: %v", err)
	}
	if _, err := l.TryLockExclusive(); !errors.Is(err, workflow.ErrBusy) {
		t.Fatalf("maintenance ran under two publications: %v", err)
	}
	a()
	if _, err := l.TryLockExclusive(); !errors.Is(err, workflow.ErrBusy) {
		t.Fatalf("maintenance ran under the remaining publication: %v", err)
	}
	b()
	unlock, err := l.TryLockExclusive()
	if err != nil {
		t.Fatalf("maintenance is refused on an idle checkout: %v", err)
	}
	if _, err := other.TryLockShared(); !errors.Is(err, workflow.ErrBusy) {
		t.Fatalf("a publication started during maintenance: %v", err)
	}
	unlock()
	if err := unlock(); err != nil {
		t.Fatalf("a second unlock must be harmless: %v", err)
	}
}

func TestLockWaitHonoursItsContext(t *testing.T) {
	l, _ := newLocker(t)
	hold, err := l.LockExclusive(deadline(t))
	if err != nil {
		t.Fatal(err)
	}
	defer hold()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := l.LockShared(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("the wait ignored its deadline")
	}
}

// An artifact published but not yet referenced by a committed database row is
// protected only by the shared publication lock.
func TestArtifactsStayProtectedUntilTheirReferencesCommit(t *testing.T) {
	l, root := newLocker(t)
	store, err := artifact.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := deadline(t)
	release, err := l.LockShared(ctx)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := store.Put(ctx, bytes.NewReader([]byte("not yet referenced")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.TryLockExclusive(); !errors.Is(err, workflow.ErrBusy) {
		t.Fatalf("garbage collection could start between Put and the database commit: %v", err)
	}
	release() // the references have committed
	gc, err := l.TryLockExclusive()
	if err != nil {
		t.Fatalf("collection is refused after the commit: %v", err)
	}
	gc()
	if _, err := store.Read(ctx, hash, 0, 1); err != nil {
		t.Fatal(err)
	}
}

// TestLockHelperProcess is not a test: it is the child the next test starts.
func TestLockHelperProcess(t *testing.T) {
	root := os.Getenv("STATE_LOCK_HELPER_ROOT")
	if root == "" {
		t.Skip("helper process")
	}
	l, err := OpenLocker(root)
	if err != nil {
		t.Fatal(err)
	}
	var unlock Unlock
	if os.Getenv("STATE_LOCK_HELPER_MODE") == "exclusive" {
		unlock, err = l.LockExclusive(context.Background())
	} else {
		unlock, err = l.LockShared(context.Background())
	}
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	os.Stdout.WriteString("locked\n")
	bufio.NewReader(os.Stdin).ReadByte() // hold until the parent closes stdin or dies
}

func startHolder(t *testing.T, root, mode string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestLockHelperProcess$")
	cmd.Env = append(os.Environ(), "STATE_LOCK_HELPER_ROOT="+root, "STATE_LOCK_HELPER_MODE="+mode)
	in, _ := cmd.StdinPipe()
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { in.Close(); cmd.Process.Kill(); cmd.Wait() })
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil || line != "locked\n" {
		t.Fatalf("helper said %q: %v", line, err)
	}
	return cmd
}

// A goroutine mutex would pass a same-process test; flock between two
// processes is the real contract.
func TestLockExcludesAnotherProcessAndSurvivesItsCrash(t *testing.T) {
	l, root := newLocker(t)
	holder := startHolder(t, root, "shared")
	if _, err := l.TryLockExclusive(); !errors.Is(err, workflow.ErrBusy) {
		t.Fatalf("maintenance ran while another process was publishing: %v", err)
	}
	shared, err := l.TryLockShared()
	if err != nil {
		t.Fatalf("two publishing processes must coexist: %v", err)
	}
	shared()
	// The process dies without unlocking, as in a crash.
	holder.Process.Kill()
	holder.Wait()
	unlock, err := l.TryLockExclusive()
	if err != nil {
		t.Fatalf("a dead process still holds the lock: %v", err)
	}
	unlock()

	holder = startHolder(t, root, "exclusive")
	if _, err := l.TryLockShared(); !errors.Is(err, workflow.ErrBusy) {
		t.Fatalf("publication started while another process ran maintenance: %v", err)
	}
}

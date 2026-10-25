//go:build darwin || linux

package state

// Operation locks for one checkout's state root, built on flock(2).
//
// Publishing an artifact and recording its database references is not one
// atomic step, so a publisher holds the shared lock across both. Maintenance
// (garbage collection) needs the exclusive lock, which is refused while any
// publisher is active. The kernel drops a dead process's locks, so a crash
// never blocks maintenance forever; it also never lets maintenance take over
// an agent whose state is uncertain, because that is a database fact, not a
// lock fact.
//
// Never make a network call to an agent while holding one of these.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/emsnoel/tandem/internal/workflow"
)

// Unlock releases a lock. Calling it again is harmless.
type Unlock func() error

// Locker hands out locks on one lock file.
type Locker struct{ path string }

// OpenLocker prepares <root>/locks/operation.lock.
func OpenLocker(root string) (*Locker, error) { return OpenLockerNamed(root, "operation") }

// OpenLockerNamed prepares <root>/locks/<name>.lock, for a lock that must not
// share the checkout's operation lock, such as one tick of one run at a time.
func OpenLockerNamed(root, name string) (*Locker, error) {
	if name == "" || strings.ContainsAny(name, "/\\\x00") || name == "." || name == ".." {
		return nil, fmt.Errorf("%w: %q is not a lock name", workflow.ErrInvalidTransition, name)
	}
	dir := filepath.Join(root, "locks")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Locker{path: filepath.Join(dir, name+".lock")}, nil
}

const lockPoll = 5 * time.Millisecond

// LockShared takes the publication lock, waiting until ctx ends.
func (l *Locker) LockShared(ctx context.Context) (Unlock, error) {
	return l.wait(ctx, syscall.LOCK_SH)
}

// LockExclusive takes the maintenance lock, waiting until ctx ends.
func (l *Locker) LockExclusive(ctx context.Context) (Unlock, error) {
	return l.wait(ctx, syscall.LOCK_EX)
}

// TryLockShared fails with workflow.ErrBusy instead of waiting.
func (l *Locker) TryLockShared() (Unlock, error) { return l.try(syscall.LOCK_SH) }

// TryLockExclusive fails with workflow.ErrBusy instead of waiting.
func (l *Locker) TryLockExclusive() (Unlock, error) { return l.try(syscall.LOCK_EX) }

func (l *Locker) try(how int) (Unlock, error) {
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		err = syscall.Flock(int(f.Fd()), how|syscall.LOCK_NB)
		if err != syscall.EINTR {
			break
		}
	}
	if err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, workflow.ErrBusy
		}
		return nil, err
	}
	var once sync.Once
	return func() (err error) {
		once.Do(func() {
			// Closing the descriptor drops the lock; the explicit unlock makes
			// the intent plain and surfaces an error if there is one.
			err = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
		})
		return err
	}, nil
}

// wait polls try, because a blocking flock cannot be cancelled by a context.
func (l *Locker) wait(ctx context.Context, how int) (Unlock, error) {
	for {
		unlock, err := l.try(how)
		if !errors.Is(err, workflow.ErrBusy) {
			return unlock, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(lockPoll):
		}
	}
}

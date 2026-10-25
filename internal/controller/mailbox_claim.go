package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/emsnoel/tandem/internal/workflow"
	"golang.org/x/sys/unix"
)

// Disposition is what ProcessClaimed did with a message.
type Disposition string

const (
	Committed   Disposition = "committed"
	Duplicate   Disposition = "duplicate"
	Quarantined Disposition = "quarantined"
)

// Result describes one processed message.
type Result struct {
	Disposition Disposition
	EventID     string
	Receipt     workflow.Receipt
	// Reason is the machine code for a quarantined message.
	Reason string
}

// ProcessClaimed handles one claimed queue file end to end. The order is the
// contract: validate, import, apply, write the receipt, then remove the claimed
// file, so a crash at any point either leaves the file for a safe replay or
// leaves nothing to replay. A transient failure returns an error and leaves the
// file; a permanent rejection quarantines it.
func ProcessClaimed(ctx context.Context, deps MailboxDeps, claimed string) (Result, error) {
	raw, err := readClaimed(claimed)
	if err != nil {
		var rej *RejectError
		if errors.As(err, &rej) {
			return quarantine(deps, claimed, rej.Code, rej.Msg)
		}
		return Result{}, err
	}
	rec, err := IngestMailbox(ctx, deps, raw)
	if err != nil {
		if code, ok := permanent(err); ok {
			return quarantine(deps, claimed, code, err.Error())
		}
		return Result{}, err
	}
	if err := writeReceipt(deps.Dir, rec); err != nil {
		return Result{}, err
	}
	if deps.afterReceipt != nil {
		if err := deps.afterReceipt(); err != nil {
			return Result{}, err
		}
	}
	if err := os.Remove(claimed); err != nil && !os.IsNotExist(err) {
		return Result{}, err
	}
	d := Committed
	if rec.Duplicate {
		d = Duplicate
	}
	return Result{Disposition: d, EventID: rec.EventID, Receipt: rec}, nil
}

// permanent reports whether err means this message can never be applied, and
// its code. Busy, I/O and store failures are not the message's fault.
func permanent(err error) (string, bool) {
	var rej *RejectError
	switch {
	case errors.As(err, &rej):
		return rej.Code, true
	case errors.Is(err, workflow.ErrInvalidTransition):
		return "invalid_transition", true
	case errors.Is(err, workflow.ErrIncompleteEvidence):
		return "incomplete_evidence", true
	case errors.Is(err, workflow.ErrStaleAttempt):
		return "stale_attempt", true
	}
	return "", false
}

func readClaimed(path string) ([]byte, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, reject("malformed_envelope", "the claimed file is a symlink")
		}
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		return nil, err
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFREG {
		return nil, reject("malformed_envelope", "the claimed file is not a regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(f, MaxEnvelopeBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxEnvelopeBytes {
		return nil, reject("malformed_envelope", "the envelope is over %d bytes", MaxEnvelopeBytes)
	}
	return raw, nil
}

// quarantine moves a message that cannot be applied out of the queue, keeping
// it as evidence with a short reason beside it.
func quarantine(deps MailboxDeps, claimed, code, msg string) (Result, error) {
	dir := filepath.Join(deps.Dir, "quarantine")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Result{}, err
	}
	base := filepath.Base(claimed)
	dest := filepath.Join(dir, base)
	for i := 1; ; i++ {
		if _, err := os.Lstat(dest); os.IsNotExist(err) {
			break
		}
		dest = filepath.Join(dir, fmt.Sprintf("%s.%d", base, i))
	}
	if err := os.Rename(claimed, dest); err != nil {
		return Result{}, err
	}
	reason := code + ": " + msg
	if len(reason) > 500 {
		reason = reason[:500]
	}
	if err := os.WriteFile(dest+".reason", []byte(reason+"\n"), 0o644); err != nil {
		return Result{}, err
	}
	return Result{Disposition: Quarantined, Reason: code}, nil
}

// writeReceipt records the applied event atomically. It is written before the
// claimed file is removed, so a replay finds it.
func writeReceipt(dir string, rec workflow.Receipt) error {
	body, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(dir, "receipts"), rec.EventID+".json", append(body, '\n'))
}

func atomicWrite(dir, name string, body []byte) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+name+".*")
	if err != nil {
		return err
	}
	cleanup := func(err error) error { tmp.Close(); os.Remove(tmp.Name()); return err }
	if _, err := tmp.Write(body); err != nil {
		return cleanup(err)
	}
	if err := tmp.Sync(); err != nil {
		return cleanup(err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, name)); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// IngestClaimed processes one claimed queue file the way a tick does: a
// submission is applied only after the controller captures the end snapshot
// and patch, and the builder's turn is judged settled by the run's transport.
// The relay bridge ingests through this (`tandem build event ingest`).
func (c *Controller) IngestClaimed(ctx context.Context, runID, claimed string) (Result, error) {
	run, cfg, err := c.load(ctx, runID)
	if err != nil {
		return Result{}, err
	}
	return ProcessClaimed(ctx, c.mailboxDeps(run, cfg), claimed)
}

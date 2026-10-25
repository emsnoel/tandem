package controller

// Reading a builder's or reviewer's payload out of its mailbox: no symlink is
// followed at any component, size and hash are verified before the bytes are
// stored as an immutable artifact.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// importPayload reads the payload with no symlink followed at any component,
// verifies its size and hash, and stores it as an artifact.
func importPayload(ctx context.Context, deps MailboxDeps, env Envelope) (string, error) {
	body, err := readPayload(deps, env)
	if err != nil {
		return "", err
	}
	return storePayload(ctx, deps, body)
}

// readPayload reads and verifies a payload without storing it, so a caller can
// look at the bytes before they become an artifact.
func readPayload(deps MailboxDeps, env Envelope) ([]byte, error) {
	if deps.beforeOpen != nil {
		deps.beforeOpen()
	}
	f, err := openNoFollow(deps.Dir, env.PayloadPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	body, err := io.ReadAll(io.LimitReader(f, MaxPayloadBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > MaxPayloadBytes {
		return nil, reject("payload_too_large", "the payload is over %d bytes", MaxPayloadBytes)
	}
	if sum := sha256Hex(body); sum != env.PayloadHash {
		return nil, reject("payload_hash_mismatch", "the payload hashes to %s, the envelope says %s", sum, env.PayloadHash)
	}
	return body, nil
}

func storePayload(ctx context.Context, deps MailboxDeps, body []byte) (string, error) {
	hash, err := deps.Artifacts.Put(ctx, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	return hash, deps.State.AdoptArtifact(ctx, hash)
}

// openNoFollow opens rel under dir one component at a time with O_NOFOLLOW, so
// a symlink swapped in at any point after validation is refused rather than
// followed. The result is a regular file.
func openNoFollow(dir, rel string) (*os.File, error) {
	bad := func(err error) error {
		if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) || errors.Is(err, unix.ENOENT) ||
			errors.Is(err, unix.EISDIR) || errors.Is(err, unix.ENXIO) {
			return reject("invalid_payload_path", "the payload path does not resolve to a regular file inside the mailbox (%v)", err)
		}
		return err
	}
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, bad(err)
	}
	parts := strings.Split(rel, "/")
	for i, part := range parts {
		flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC
		if i < len(parts)-1 {
			flags |= unix.O_DIRECTORY
		} else {
			flags |= unix.O_NONBLOCK // a FIFO must not hang the open
		}
		next, err := unix.Openat(fd, part, flags, 0)
		unix.Close(fd)
		if err != nil {
			return nil, bad(err)
		}
		fd = next
	}
	f := os.NewFile(uintptr(fd), rel)
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		f.Close()
		return nil, err
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFREG {
		f.Close()
		return nil, reject("invalid_payload_path", "the payload is not a regular file")
	}
	return f, nil
}

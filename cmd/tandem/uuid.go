package main

import (
	"crypto/rand"
	"fmt"
)

// newSessionID returns a random RFC 4122 version 4 UUID. It lives outside
// internal/agent, which must stay deterministic. It must stay random: a
// collision would make Copilot resume an existing session.
func newSessionID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

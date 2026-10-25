package main

import (
	"regexp"
	"testing"
)

var uuidV4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestNewSessionIDShape(t *testing.T) {
	id, err := newSessionID()
	if err != nil {
		t.Fatal(err)
	}
	if !uuidV4.MatchString(id) {
		t.Fatalf("got %q, want a v4 UUID", id)
	}
}

func TestNewSessionIDIsFreshEachCall(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		id, err := newSessionID()
		if err != nil {
			t.Fatal(err)
		}
		if seen[id] {
			t.Fatalf("repeated id %q", id)
		}
		seen[id] = true
	}
}

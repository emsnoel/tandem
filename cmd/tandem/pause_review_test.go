package main

import (
	"strings"
	"testing"
)

func TestReplyLogAgyNamesFolder(t *testing.T) {
	got := replyLog("agy")
	for _, want := range []string{"antigravity-cli", "conversations", ".db"} {
		if !strings.Contains(got, want) {
			t.Errorf("replyLog(agy) = %q, missing %q", got, want)
		}
	}
}

package main

import (
	"strings"
	"testing"
)

func TestReplyLogAgyNamesFolderAndLimit(t *testing.T) {
	got := replyLog("agy")
	for _, want := range []string{"~/.gemini/antigravity-cli/conversations", "can't read those files yet"} {
		if !strings.Contains(got, want) {
			t.Errorf("replyLog(agy) = %q, missing %q", got, want)
		}
	}
}

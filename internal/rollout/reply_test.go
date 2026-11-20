package rollout

import (
	"strings"
	"testing"
)

// The log holds each reply whole, however the terminal showed it; prompts,
// tool output and a line still being written are not replies.
func TestRepliesAreTheAssistantMessagesInOrder(t *testing.T) {
	log := strings.Join([]string{
		`{"type":"session_meta","payload":{"id":"s"}}`,
		prompt(`reply with {\"job_id\":\"j\"}`),
		`{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"first"}]}}`,
		`{"type":"event_msg","payload":{"type":"agent_message","message":"first"}}`,
		`{"type":"response_item","payload":{"type":"function_call_output","output":"assistant"}}`,
		`{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"{\"job_id\":\"j\"}"}]}}`,
		`{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_te`,
	}, "\n")
	got, err := Replies(strings.NewReader(log))
	if err != nil || got != "first\n{\"job_id\":\"j\"}\n" {
		t.Fatalf("got %q, %v", got, err)
	}
}

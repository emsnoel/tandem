package rollout

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
)

// Replies is the text of every assistant message in a rollout log, oldest
// first, one message after another. It is what the agent said, whole, however
// its terminal showed or collapsed it. A line that does not decode is skipped:
// the last one may still be being written.
func Replies(r io.Reader) (string, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	var b strings.Builder
	for sc.Scan() {
		line := sc.Bytes()
		if !strings.Contains(string(line), `"assistant"`) {
			continue
		}
		var e struct {
			Type    string `json:"type"`
			Payload struct {
				Type    string `json:"type"`
				Role    string `json:"role"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &e) != nil || e.Type != "response_item" || e.Payload.Type != "message" || e.Payload.Role != "assistant" {
			continue
		}
		for _, c := range e.Payload.Content {
			if c.Type == "output_text" {
				b.WriteString(c.Text)
				b.WriteString("\n")
			}
		}
	}
	return b.String(), sc.Err()
}

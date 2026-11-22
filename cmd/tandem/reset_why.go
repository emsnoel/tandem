package main

// What an unconfirmed reset saw, for the person who reads the pause.
//
// A reset that proves nothing in its time reports the token "unconfirmed".
// The token alone did not say why #116 and #118 failed, so the pause also
// says what Herdr, the kind's own log and the screen showed at the end.

import (
	"fmt"
	"strings"
	"time"

	"github.com/emsnoel/tandem/internal/agent"
)

// screenTailLines is how many non-empty lines of the screen unconfirmedWhy
// quotes, and screenTailRunes the most it quotes in all.
const (
	screenTailLines = 3
	screenTailRunes = 240
)

// unconfirmedWhy reads name's pane once more and says what it shows: Herdr's
// status, whether kind's own log has a conversation from since on, whether
// the screen shows a fresh conversation, and the screen's last lines.
func unconfirmedWhy(d resetDeps, kind, name string, since time.Time) string {
	var parts []string
	measured := false
	if b, err := d.get(name); err != nil {
		parts = append(parts, fmt.Sprintf("Herdr could not read the pane (%v)", err))
	} else {
		parts = append(parts, fmt.Sprintf("Herdr reports it %s", b.StatusString()))
		var s string
		if s, measured = d.fresh(kind, b, since); measured && s == "" {
			parts = append(parts, fmt.Sprintf("%s's own log shows no new conversation", kind))
		}
	}
	if d.screen != nil {
		if text, err := d.screen(name); err != nil {
			parts = append(parts, fmt.Sprintf("its screen could not be read (%v)", err))
		} else {
			if measured && !agent.FreshScreen(kind, text) {
				parts = append(parts, "its screen shows no fresh conversation")
			}
			if tail := screenTail(text); tail != "" {
				parts = append(parts, fmt.Sprintf("its screen ends %q", tail))
			}
		}
	}
	return strings.Join(parts, "; ")
}

// screenTail is the screen's last non-empty lines, top to bottom, with runs of
// spaces collapsed, joined by " / " and cut to screenTailRunes.
func screenTail(text string) string {
	lines := strings.Split(text, "\n")
	var tail []string
	for i := len(lines) - 1; i >= 0 && len(tail) < screenTailLines; i-- {
		if t := strings.Join(strings.Fields(lines[i]), " "); t != "" {
			tail = append([]string{t}, tail...)
		}
	}
	out := []rune(strings.Join(tail, " / "))
	if len(out) > screenTailRunes {
		return string(out[:screenTailRunes-1]) + "…"
	}
	return string(out)
}

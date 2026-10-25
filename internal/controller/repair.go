package controller

// What a repair attempt is told. A repair is a new attempt in a fresh
// conversation, so it is given the reviewer's findings that are still open and
// the files the previous attempt changed, not the previous conversation. The
// text is bounded, and the full review stays readable through the artifact
// protocol for the new attempt.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/emsnoel/tandem/internal/workflow"
)

const (
	// repairFindingsBytes bounds the findings text in a repair's note, whatever
	// the reviewer wrote; the rest is named, not dropped.
	repairFindingsBytes = 2000
	// repairFindingBytes bounds one finding's message.
	repairFindingBytes = 300
	repairPaths        = 20
)

// repairContext is the note a repair attempt's prompt opens with and the
// artifacts it is allowed to read.
type repairContext struct {
	Note string
	Refs [][2]string
}

// repairFor builds the context for the attempt that repairs parent.
func (c *Controller) repairFor(ctx context.Context, parent workflow.Attempt) (repairContext, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "This is attempt %d. The reviewer asked for changes to attempt %d; its work is still in the tree. This is a fresh conversation: fix what the reviewer found, starting from what follows.",
		parent.Ordinal+1, parent.Ordinal)
	var rc repairContext

	if hash, found, err := c.d.State.LatestReviewResult(ctx, parent.ID); err != nil {
		return rc, err
	} else if found {
		raw, err := c.d.Artifacts.ReadAll(ctx, hash, 1<<20)
		if err != nil {
			return rc, err
		}
		var res workflow.ReviewResult
		if err := json.Unmarshal(raw, &res); err != nil {
			return rc, fmt.Errorf("%w: the review of attempt %s is unreadable: %v", workflow.ErrCorruptArtifact, parent.ID, err)
		}
		var open []workflow.Finding
		for _, f := range res.Findings {
			if f.Disposition == "open" {
				open = append(open, f)
			}
		}
		if len(open) > 0 {
			fmt.Fprintf(&b, "\n\nOpen findings from the review of attempt %d (%d):\n", parent.Ordinal, len(open))
			used, shown := 0, 0
			for _, f := range open {
				line := "- " + findingLine(f) + "\n"
				if used+len(line) > repairFindingsBytes {
					break
				}
				b.WriteString(line)
				used += len(line)
				shown++
			}
			if shown < len(open) {
				fmt.Fprintf(&b, "(%d more open findings are in the full review: `tandem artifact read %s --attempt ATTEMPT`, where ATTEMPT is in the Context line.)\n", len(open)-shown, hash)
			}
		}
		rc.Refs = append(rc.Refs, [2]string{"parent_review", hash})
	}

	if parent.PatchHash != "" {
		if paths, err := c.changedPaths(ctx, parent.PatchHash); err == nil && len(paths) > 0 {
			shown := paths
			more := 0
			if len(shown) > repairPaths {
				shown, more = shown[:repairPaths], len(shown)-repairPaths
			}
			fmt.Fprintf(&b, "\nFiles attempt %d changed: %s", parent.Ordinal, strings.Join(shown, ", "))
			if more > 0 {
				fmt.Fprintf(&b, " and %d more", more)
			}
			b.WriteString(".\n")
		}
	}
	rc.Note = strings.TrimRight(b.String(), "\n")
	return rc, nil
}

func findingLine(f workflow.Finding) string {
	loc := f.Path
	if f.StartLine != nil {
		loc += fmt.Sprintf(":%d", *f.StartLine)
		if f.EndLine != nil && *f.EndLine != *f.StartLine {
			loc += fmt.Sprintf("-%d", *f.EndLine)
		}
	}
	msg := strings.Join(strings.Fields(f.Message), " ")
	if len(msg) > repairFindingBytes {
		cut := repairFindingBytes
		for cut > 0 && !utf8.RuneStart(msg[cut]) {
			cut--
		}
		msg = msg[:cut] + "..."
	}
	return fmt.Sprintf("[%s] %s: %s", f.Severity, loc, msg)
}

// changedPaths lists the paths a patch changes, in the patch's order.
func (c *Controller) changedPaths(ctx context.Context, patchHash string) ([]string, error) {
	raw, err := c.d.Artifacts.ReadAll(ctx, patchHash, 8<<20)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Changes []struct {
			Path    string `json:"path"`
			PathB64 string `json:"path_b64"`
		} `json:"changes"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	var out []string
	for _, ch := range doc.Changes {
		if ch.Path != "" {
			out = append(out, ch.Path)
		}
	}
	return out, nil
}

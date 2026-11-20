package controller

// What a repair attempt is told. A repair is a new attempt in a fresh
// conversation, so it is given the end of the failed check's log or the
// reviewer's findings that are still open, and the files the previous attempt
// changed, not the previous conversation. The text is bounded, and the full
// log or review stays readable through the artifact protocol for the new
// attempt.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/emsnoel/tandem/internal/state"
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
	lead := fmt.Sprintf("This is attempt %d. The reviewer asked for changes to attempt %d; its work is still in the tree. This is a fresh conversation: fix what the reviewer found, starting from what follows.",
		parent.Ordinal+1, parent.Ordinal)
	if parent.Check == workflow.CheckFailed {
		lead = fmt.Sprintf("This is attempt %d. The check failed on attempt %d; its work is still in the tree. This is a fresh conversation: make the check pass, starting from what follows.",
			parent.Ordinal+1, parent.Ordinal)
	}
	return c.repairNote(ctx, parent, lead)
}

// repairNote is lead, then what parent left open: the end of its failed
// check's log, the reviewer's open findings and the files it changed.
func (c *Controller) repairNote(ctx context.Context, parent workflow.Attempt, lead string) (repairContext, error) {
	var b strings.Builder
	var rc repairContext
	b.WriteString(lead)
	if parent.Check == workflow.CheckFailed {
		ev, err := c.checkFailure(ctx, parent)
		if err != nil {
			return rc, err
		}
		if ev.Excerpt == "" {
			b.WriteString("\n\nThe check printed nothing. Run the project's check command to see why it fails.\n")
		} else {
			fmt.Fprintf(&b, "\n\nThe end of the check's log (the whole log is `tandem artifact read %s --attempt ATTEMPT`, where ATTEMPT is in the Context line):\n%s\n", ev.Hash, ev.Excerpt)
		}
		rc.Refs = append(rc.Refs, [2]string{"parent_check_log", ev.Hash})
	}

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

// checkFailure reads the failed check of parent: the hash of its log and the
// end of that log, bounded. It refuses an attempt whose check did not fail or
// left no complete certificate.
func (c *Controller) checkFailure(ctx context.Context, parent workflow.Attempt) (failureEvidence, error) {
	notShown := fmt.Errorf("%w: attempt %s does not show a %s failure", workflow.ErrIncompleteEvidence, parent.ID, state.CauseCheckFailure)
	if parent.Check != workflow.CheckFailed {
		return failureEvidence{}, notShown
	}
	cert, found, err := c.d.State.AttemptCheckCertificate(ctx, parent.ID)
	if err != nil {
		return failureEvidence{}, err
	}
	if !found || !cert.Complete || cert.ExitCode == 0 {
		return failureEvidence{}, notShown
	}
	log, err := c.d.Artifacts.ReadAll(ctx, cert.LogHash, 64<<20)
	if err != nil {
		return failureEvidence{}, err
	}
	return failureEvidence{Hash: cert.LogHash, Excerpt: boundExcerpt(string(log), false)}, nil
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

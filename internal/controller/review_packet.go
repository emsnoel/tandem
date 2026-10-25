package controller

// The reviewer packet.
//
// A reviewer gets everything the acceptance policy says it needs: the task
// brief and constraints, the evidence of tasks it depends on, the full check
// result, the patch and tree under review and the job's identity. A diff alone
// cannot show what was asked for.
//
// Anything cut for size is marked and names the artifact holding the whole,
// readable with `tandem artifact read HASH --attempt ID`. Both stages share one
// body; only the closing delivery block differs between protocols.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

const (
	// maxPacketPatchBytes and maxPacketLogBytes bound what is inlined.
	maxPacketPatchBytes = 16 << 10
	maxPacketLogBytes   = 4 << 10
)

// DependencyEvidence is what an accepted task this one depends on left behind.
type DependencyEvidence struct {
	TaskID, AttemptID, PatchHash, TreeHash string
}

// CheckEvidence is the full result of the run's declared check.
type CheckEvidence struct {
	Command              string
	Ran                  bool
	ExitCode             int
	Complete             bool
	LogHash              string
	LogExcerpt           string
	LogTruncated         bool
	CertificateID, JobID string
}

// ReviewPacket is everything one review stage is shown.
type ReviewPacket struct {
	Stage                   string
	RunID, AttemptID, JobID string
	EffectID, Nonce         string
	TaskOrdinal, TaskTotal  int
	TaskSlug                string
	Brief, Constraints      string
	Dependencies            []DependencyEvidence
	Check                   CheckEvidence
	PatchHash, TreeHash     string
	Patch                   string
	PatchTruncated          bool
	// WarmPane says the reviewer is a pane that keeps earlier tasks in its
	// context, and is told to judge this packet on its own.
	WarmPane    bool
	HandbackDir string
}

// buildReviewPacket gathers a stage's packet from persisted evidence. It fails
// rather than leave a mandatory part out.
func (c *Controller) buildReviewPacket(ctx context.Context, run state.Run, cfg runConfig, task state.TaskRow, att workflow.Attempt, jobID, stage string) (ReviewPacket, error) {
	p := ReviewPacket{Stage: stage, RunID: run.ID, AttemptID: att.ID, JobID: jobID, TaskOrdinal: task.Ordinal, TaskSlug: task.ID,
		PatchHash: att.PatchHash, TreeHash: att.EndSnapshotHash}
	if att.PatchHash == "" || att.EndSnapshotHash == "" {
		return p, fmt.Errorf("%w: attempt %s has no patch and tree to review", workflow.ErrIncompleteEvidence, att.ID)
	}
	tasks, err := c.d.State.RunTasks(ctx, run.ID)
	if err != nil {
		return p, err
	}
	p.TaskTotal = len(tasks)
	raw, err := c.d.Artifacts.ReadAll(ctx, task.SpecHash, 4<<20)
	if err != nil {
		return p, err
	}
	var spec workflow.TaskSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		return p, fmt.Errorf("%w: the spec of task %s is unreadable: %v", workflow.ErrCorruptArtifact, task.ID, err)
	}
	p.Brief, p.Constraints = spec.Brief, spec.Constraints
	for _, dep := range spec.DependsOn {
		var found bool
		for _, t := range tasks {
			if t.ID != dep {
				continue
			}
			found = true
			if t.AcceptedAttemptID == "" {
				return p, fmt.Errorf("%w: task %s depends on %s, which is not accepted", workflow.ErrIncompleteEvidence, task.ID, dep)
			}
			da, err := c.d.State.GetAttempt(ctx, t.AcceptedAttemptID)
			if err != nil {
				return p, err
			}
			p.Dependencies = append(p.Dependencies, DependencyEvidence{TaskID: dep, AttemptID: da.ID, PatchHash: da.PatchHash, TreeHash: da.EndSnapshotHash})
		}
		if !found {
			return p, fmt.Errorf("%w: task %s depends on unknown task %s", workflow.ErrIncompleteEvidence, task.ID, dep)
		}
	}
	if p.Check, err = c.checkEvidence(ctx, cfg, att); err != nil {
		return p, err
	}
	text, truncated, err := c.patchBody(ctx, att)
	if err != nil {
		return p, err
	}
	p.Patch, p.PatchTruncated = text, truncated
	return p, nil
}

// checkEvidence is the declared check's result for the attempt. A run with no
// declared check says so; a run with one and no certificate is incomplete.
func (c *Controller) checkEvidence(ctx context.Context, cfg runConfig, att workflow.Attempt) (CheckEvidence, error) {
	if cfg.CheckCommand == "" {
		return CheckEvidence{}, nil
	}
	ev := CheckEvidence{Command: cfg.CheckCommand, Ran: true}
	cert, found, err := c.d.State.AttemptCheckCertificate(ctx, att.ID)
	if err != nil {
		return ev, err
	}
	if !found {
		return ev, fmt.Errorf("%w: the declared check has no result for attempt %s", workflow.ErrIncompleteEvidence, att.ID)
	}
	ev.ExitCode, ev.Complete, ev.LogHash, ev.CertificateID, ev.JobID = cert.ExitCode, cert.Complete, cert.LogHash, cert.ID, cert.JobID
	log, err := c.d.Artifacts.ReadAll(ctx, cert.LogHash, 64<<20)
	if err != nil {
		return ev, err
	}
	if len(log) > maxPacketLogBytes {
		ev.LogExcerpt, ev.LogTruncated = string(log[len(log)-maxPacketLogBytes:]), true // the end of a log is the part that fails
	} else {
		ev.LogExcerpt = string(log)
	}
	return ev, nil
}

var stageFraming = map[string]string{
	stageReview: "You are the reviewer. Review the work on this task. Look hard for what the brief did not ask for as well as what it did.",
	stageIndependentVerification: "You are the independent verifier. Verify this work from your own understanding of the plan and the project, in your original context. " +
		"The reviewer pane's review is not part of this packet and is not evidence: do not rely on it, and do not pass work because it was already reviewed.",
}

// resultFormat is the review result, the same for both stages and both
// protocols: the identity it names must match the job, the patch and the tree.
func resultFormat(p ReviewPacket) string {
	return fmt.Sprintf("Your result is ONE JSON object:\n\n"+
		"{\"schema_version\":1,\"job_id\":%q,\"attempt_id\":%q,\"patch_hash\":%q,\"tree_hash\":%q,\n"+
		" \"status\":\"passed|changes_requested|failed\",\n"+
		" \"findings\":[{\"severity\":\"high|medium|low\",\"path\":\"file\",\"disposition\":\"open\",\"message\":\"what is wrong\",\"start_line\":null,\"end_line\":null}]}\n\n"+
		"changes_requested must list at least one finding. A result that names another job, patch or tree is refused. Do not edit files.",
		p.JobID, p.AttemptID, p.PatchHash, p.TreeHash)
}

// deliveryBlock is the only part of a packet that depends on the protocol.
func deliveryBlock(p ReviewPacket, protocol int) string {
	if protocol < 3 {
		return "Reply with the JSON object and nothing else."
	}
	return fmt.Sprintf("When you begin, acknowledge with:\n\n    %s --started\n\nWrite your verdict to %s, then run:\n\n    %s\n\nIf the command rejects it, correct the verdict and retry in this turn. Each call returns immediately. Then stop.",
		shellQuote(p.HandbackDir+"/handback"), shellQuote(p.HandbackDir+"/verdict.json"), shellQuote(p.HandbackDir+"/handback"))
}

// renderReviewPacket writes the packet for a protocol.
func renderReviewPacket(p ReviewPacket, protocol int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", stageFraming[p.Stage])
	if p.WarmPane {
		fmt.Fprintf(&b, "%s\n\n", judgeAlone)
	}
	fmt.Fprintf(&b, "== Packet\nstage: %s\nrun: %s\nattempt: %s\njob: %s\n\n", p.Stage, p.RunID, p.AttemptID, p.JobID)
	fmt.Fprintf(&b, "== Task %d of %d: %s\n\nBrief (exact):\n%s\n\nConstraints (exact):\n%s\n\n", p.TaskOrdinal, p.TaskTotal, p.TaskSlug,
		noneIfBlank(p.Brief), noneIfBlank(p.Constraints))
	b.WriteString("== Dependencies\n")
	if len(p.Dependencies) == 0 {
		b.WriteString("none\n")
	}
	for _, d := range p.Dependencies {
		fmt.Fprintf(&b, "- %s: accepted attempt %s, patch %s, tree %s\n", d.TaskID, d.AttemptID, d.PatchHash, d.TreeHash)
	}
	b.WriteString("\n== Checks\n")
	switch {
	case !p.Check.Ran:
		b.WriteString("the run declares no check\n")
	default:
		fmt.Fprintf(&b, "command: %s\nexit code: %d\ncomplete: %t\ncertificate: %s\n", p.Check.Command, p.Check.ExitCode, p.Check.Complete, p.Check.CertificateID)
		fmt.Fprintf(&b, "log:\n%s\n", p.Check.LogExcerpt)
		if p.Check.LogTruncated {
			fmt.Fprintf(&b, "[log truncated: only its last %d bytes are shown. The whole log is artifact %s: tandem artifact read %s --attempt %s]\n",
				maxPacketLogBytes, p.Check.LogHash, p.Check.LogHash, p.AttemptID)
		}
	}
	fmt.Fprintf(&b, "\n== Evidence under review\npatch: %s\ntree: %s\n\n%s\n", p.PatchHash, p.TreeHash, p.Patch)
	if p.PatchTruncated {
		fmt.Fprintf(&b, "[patch truncated: only its first %d bytes are shown. The whole patch is artifact %s: tandem artifact read %s --attempt %s]\n",
			maxPacketPatchBytes, p.PatchHash, p.PatchHash, p.AttemptID)
	}
	fmt.Fprintf(&b, "\n== Result\n%s\n\n== Delivery\n%s\n", packetResultFormat(p, protocol), deliveryBlock(p, protocol))
	return b.String()
}

func noneIfBlank(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(none)"
	}
	return s
}

func packetResultFormat(p ReviewPacket, protocol int) string {
	if protocol < 3 {
		return resultFormat(p)
	}
	return "Your verdict is ONE JSON object:\n\n" +
		`{"schema_version":1,"status":"passed|changes_requested|failed","findings":[]}` +
		"\n\nchanges_requested must list at least one finding, with severity, path, disposition and message. Optional start_line and end_line are 1-based. A passed verdict cannot leave blocking findings unresolved. Tandem supplies the job, attempt, patch and tree from the stored job. Edit only the verdict file; do not edit source files."
}

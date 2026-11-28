package controller

import (
	"fmt"
	"strings"

	"github.com/emsnoel/tandem/internal/memory"
	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/taskcontext"
	"github.com/emsnoel/tandem/internal/workflow"
)

// packetRequest is the context request for one attempt of a task: the task,
// the memory it may be shown, and the controller's header and footer, which
// name the attempt's identity.
func packetRequest(cfg runConfig, runID, planHash, startSnapshot, attemptID string, ordinal, total int, spec workflow.TaskSpec, entries []memory.Entry, note string) taskcontext.Request {
	p := attemptIdentity(cfg.Root, runID, attemptID, ordinal)
	p.Total, p.Slug, p.Plan, p.Note, p.CheckCommand = total, spec.ID, cfg.PlanPath, note, cfg.CheckCommand
	p.Handback = builderHandbackPath(cfg, runID, p.Nonce)
	return taskcontext.Request{RunID: runID, AttemptID: attemptID, PlanHash: planHash, StartSnapshotHash: startSnapshot,
		Task: spec, Entries: entries, MaxEstimatedTokens: cfg.ContextLimit,
		Header: promptHeader(p) + cfg.Settings.prelude(cfg.Root), Footer: cfg.Settings.closing() + promptFooter(p)}
}

// attemptIdentity is the part of an attempt's prompt data its handback names:
// the run, the attempt, its builder job and its task prompt's effect and nonce.
func attemptIdentity(root, runID, attemptID string, task int) promptData {
	eventID := derive(attemptID, "created")
	return promptData{Task: task, Root: root, Run: runID, Attempt: attemptID, Job: derive(attemptID, "builder-job"),
		Effect: state.EffectID(eventID, 0), Nonce: state.EffectNonce(eventID, 0)}
}

type promptData struct {
	Task, Total                      int
	Slug, Plan, Root, Note           string
	Run, Attempt, Job, Effect, Nonce string
	CheckCommand                     string
	Handback                         string
}

// promptHeader says which task this is. It is part of the packet's mandatory
// text.
func promptHeader(p promptData) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Implement task %d of %d (%s) of the plan at %s. Working directory: %s.", p.Task, p.Total, p.Slug, p.Plan, p.Root)
	if p.Note != "" {
		fmt.Fprintf(&b, "\n\n%s", p.Note)
	}
	return b.String()
}

// stopConditions are the five findings the plan review looks for. A builder that
// meets one has found what the review missed, and reports it instead of
// improvising.
const stopConditions = "\n\nStop and hand back with --stuck, changing nothing further, if a step:\n" +
	"  - needs something a later task introduces;\n" +
	"  - contradicts another step;\n" +
	"  - cannot run as written;\n" +
	"  - needs authority you do not have;\n" +
	"  - has a test or example that would not give the result the plan claims.\n" +
	"Name the step and what you found in the report. Stopping then is the correct outcome, not a failure. Do not improvise."

// promptFooter gives the builder its handback command, says to acknowledge
// before touching anything, and says how to wait and when to stop. A builder
// that polls pays for its whole conversation on every look (F19).
func promptFooter(p promptData) string {
	var b strings.Builder
	command := p.handbackCommand()
	fmt.Fprintf(&b, "BEFORE YOU EDIT ANYTHING, acknowledge this prompt. It is your first action:\n\n    %s --started\n\n", command)
	b.WriteString("Do the task. Do not commit unless the brief says to.")
	if p.CheckCommand != "" {
		b.WriteString(" Tandem runs the project's check itself after you hand back; do not paste its output, and do not run the full check yourself unless the task needs its output.")
	}
	b.WriteString(" If a command or a subagent takes long, run it in ONE blocking call with the longest timeout your tool accepts. Do not poll it.")
	b.WriteString(stopConditions)
	fmt.Fprintf(&b, "\n\nWhen you finish, write a report (under 20 lines: whether the task is complete, anything you changed that the brief did not ask for, anything you could not verify) and hand it back. %s\n\n    %s --report <file>\n\nAdd --stuck if you could not finish. That call returns immediately. Then stop.", handbackRule, command)
	return b.String()
}

func (p promptData) handbackCommand() string {
	if p.Handback != "" {
		return shellQuote(p.Handback)
	}
	return "tandem-handback " + p.handbackID()
}

// handbackRule is how a builder hands back so the handback cannot hang. A
// report piped through cat with no input, in a call the agent moved to the
// background, waited forever and the handback after it never ran (#90).
const handbackRule = "Write the report to a file with your file-write tool, never through cat or a heredoc. " +
	"Then run this in the foreground, never in the background, and wait for it to return:"

// handbackID is the identity a builder's tandem-handback names. It names the
// run's root, so it finds the run from any directory (#61).
func (p promptData) handbackID() string {
	id := fmt.Sprintf("--task %d --run %s --attempt %s --job %s --effect %s --nonce %s", p.Task, p.Run, p.Attempt, p.Job, p.Effect, p.Nonce)
	if p.Root != "" {
		id = "--root " + shellQuote(p.Root) + " " + id
	}
	return id
}

type reviewPromptData struct {
	Task                          int
	Slug                          string
	Job, Attempt, PatchHash, Tree string
	Patch                         string
	TaskPaths                     []string
	WarmPane                      bool
}

// reviewPrompt asks for a structured review. The result names the job, the
// attempt and the exact patch and tree it examined; a result that names others
// is refused.
func reviewPrompt(p reviewPromptData) string {
	return renderReviewPrompt(p, "")
}

const judgeManualTask = "Judge this task on its own."

func renderReviewPrompt(p reviewPromptData, requirement string) string {
	var b strings.Builder
	if requirement == "" {
		fmt.Fprintf(&b, "Review the work on task %d (%s). Look hard for what the brief did not ask for as well as what it did.\n\n", p.Task, p.Slug)
		if p.WarmPane {
			fmt.Fprintf(&b, "%s\n\n", judgeAlone)
		}
	} else {
		fmt.Fprintf(&b, "Review task %d (%s) for missing or extra work. %s\n\n%s\n\n", p.Task, p.Slug, judgeManualTask, requirement)
	}
	fmt.Fprintf(&b, "Reply with ONE JSON object and nothing else:\n\n")
	fmt.Fprintf(&b, "{\"schema_version\":1,\"job_id\":%q,\"attempt_id\":%q,\"patch_hash\":%q,\"tree_hash\":%q,\n \"status\":\"passed|changes_requested|failed\",\n \"findings\":[{\"severity\":\"high|medium|low\",\"path\":\"file\",\"disposition\":\"open\",\"message\":\"what is wrong\",\"start_line\":null,\"end_line\":null}]}\n\n",
		p.Job, p.Attempt, p.PatchHash, p.Tree)
	b.WriteString("changes_requested must list at least one finding. Do not edit files.\n\nThe patch:\n\n")
	b.WriteString(patchOrNote(p.Patch, p.TaskPaths))
	return b.String()
}

type manualReviewCandidate struct {
	prompt, readCommand, readOutput string
}

// bytes is a lower bound for a referenced brief: the tool call and result
// add a model continuation, whose provider overhead is not known offline.
func (p manualReviewCandidate) bytes() int {
	return len(p.prompt) + len(p.readCommand) + len(p.readOutput)
}

func proposeManualReview(p reviewPromptData, spec workflow.TaskSpec, briefPath string, budget int) manualReviewCandidate {
	requirement := "Brief:\n" + spec.Brief
	if spec.Constraints != "" {
		requirement += "\n\nConstraints:\n" + spec.Constraints
	}
	inline := renderReviewPrompt(p, requirement)
	if len(inline) <= budget {
		return manualReviewCandidate{prompt: inline}
	}
	command := "cat " + shellQuote(briefPath)
	return manualReviewCandidate{
		prompt:      renderReviewPrompt(p, "Read the exact requirements before reviewing:\n    "+command),
		readCommand: command,
		readOutput:  requirement + "\n",
	}
}

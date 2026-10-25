// Package taskcontext assembles the one prompt a builder gets for a task, under
// a byte budget, and records what it left out.
//
// Mandatory content (the controller's wrapper, the task brief, the plan's
// constraints and every required fact or contract) is rendered first and
// measured exactly; if it does not fit, the build is refused with
// ErrContextTooLarge and each section's size, never trimmed. Optional content
// (facts from earlier work) goes in whole or not at all, in a fixed order,
// until the budget is used.
//
// Only facts that apply to this task are considered, so the packet does not
// grow with the number of earlier tasks. Build is pure and deterministic.
package taskcontext

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/emsnoel/tandem/internal/memory"
	"github.com/emsnoel/tandem/internal/workflow"
)

// EstimateMethod names how a prompt's size is estimated. It is the byte length
// of the UTF-8 prompt, which is at least the number of tokens in it for any
// tokenizer in use, and so is a safe upper bound, though not an exact count.
const EstimateMethod = "utf8-bytes-upper-estimate-v1"

// DefaultMaxEstimatedTokens is the budget when a request sets none.
const DefaultMaxEstimatedTokens = 24000

// schemaVersion and templateVersion are part of the configuration hash: a change
// to either is a change to what a packet means.
const (
	schemaVersion   = 1
	templateVersion = "v1"
)

// omissionNotice closes the optional facts when some did not fit. Its length is
// reserved while facts are chosen, so adding it can never push a packet over.
const omissionNotice = "Some optional context did not fit this prompt. `tandem task context --attempt ATTEMPT --explain` lists what was left out, and `tandem artifact read HASH --attempt ATTEMPT` fetches a fact's text (ATTEMPT is in the Context line).\n\n"

// Request is what a packet is built from. Entries are hydrated: each carries its
// bounded Value text and Scope.
type Request struct {
	RunID, AttemptID  string
	PlanHash          string
	StartSnapshotHash string
	Task              workflow.TaskSpec
	// Entries are the memory entries the controller loaded for the run. Only those
	// that apply to Task are shown; the rest are neither rendered nor recorded.
	Entries []memory.Entry
	// RequiredContracts are the ids of interface contracts the task must be
	// given even though they come from tasks it does not directly depend on. Each
	// must be among Entries and still active.
	RequiredContracts []string
	// MaxEstimatedTokens is the budget in bytes of prompt; 0 means the default.
	MaxEstimatedTokens int
	// Header and Footer are the controller's wrapper: what the task is, and how
	// the builder acknowledges and hands back. They are mandatory and counted.
	Header, Footer string
}

// Packet is the prompt and the record of how it was made.
type Packet struct {
	Prompt string
	// Manifest is deterministic JSON: identity, budget, sizes, selected and
	// omitted ids with reasons, the prompt hash and the configuration hash.
	Manifest        []byte
	EstimatedTokens int
	EstimateMethod  string
}

// Section is one part of a packet and its size in bytes.
type Section struct {
	Name  string `json:"name"`
	Bytes int    `json:"bytes"`
}

// TooLargeError is a packet whose mandatory content exceeds its budget.
type TooLargeError struct {
	Limit    int
	Required int
	Sections []Section
}

func (e *TooLargeError) Error() string {
	var parts []string
	for _, s := range e.Sections {
		parts = append(parts, fmt.Sprintf("%s %d", s.Name, s.Bytes))
	}
	return fmt.Sprintf("%v: the mandatory content is %d bytes and the limit is %d (%s)", workflow.ErrContextTooLarge, e.Required, e.Limit, strings.Join(parts, ", "))
}

func (e *TooLargeError) Unwrap() error { return workflow.ErrContextTooLarge }

var (
	idPattern   = regexp.MustCompile(`^[0-9a-f]{32}$`)
	hashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: task context: %s", workflow.ErrInvalidTransition, fmt.Sprintf(format, a...))
}

// Build renders the packet for a request.
func Build(req Request) (Packet, error) {
	limit := req.MaxEstimatedTokens
	switch {
	case limit < 0:
		return Packet{}, invalid("the limit cannot be negative")
	case limit == 0:
		limit = DefaultMaxEstimatedTokens
	}
	if err := validate(req); err != nil {
		return Packet{}, err
	}

	// Which entries apply is memory's decision. Everything supplied is taken to be
	// current: the controller marks an entry whose binding no longer holds as
	// invalid before it gets here, so a stale required fact is reported, not lost.
	valid := map[string]bool{}
	for _, e := range req.Entries {
		valid[e.ValidityHash] = true
	}
	explained := memory.Explain(req.Entries, req.Task, valid)
	if len(explained.StaleRequired) > 0 {
		return Packet{}, fmt.Errorf("%w: required memory %s is no longer valid, so the task cannot be rendered without it", workflow.ErrIncompleteEvidence, strings.Join(explained.StaleRequired, ", "))
	}
	selected, err := memory.Select(req.Entries, req.Task, valid)
	if err != nil {
		return Packet{}, err
	}

	byID := map[string]memory.Entry{}
	for _, e := range req.Entries {
		byID[e.ID] = e
	}
	type shown struct {
		e        memory.Entry
		reason   string
		required bool
	}
	reasons := map[string]string{}
	for _, it := range explained.Included {
		reasons[it.ID] = it.Reason
	}
	var required, optional []shown
	have := map[string]bool{}
	for _, e := range selected {
		have[e.ID] = true
		if e.Required {
			required = append(required, shown{e, reasons[e.ID], true})
		} else {
			optional = append(optional, shown{e, reasons[e.ID], false})
		}
	}
	// A contract named explicitly is mandatory wherever it came from.
	named := append([]string{}, req.RequiredContracts...)
	sort.Strings(named)
	for i, id := range named {
		if i > 0 && named[i-1] == id {
			continue
		}
		e, ok := byID[id]
		switch {
		case !ok:
			return Packet{}, fmt.Errorf("%w: required contract %s was not supplied", workflow.ErrIncompleteEvidence, id)
		case e.Kind != memory.KindInterfaceContract:
			return Packet{}, fmt.Errorf("%w: %s is a %s, and only an interface contract can be required by name", workflow.ErrIncompleteEvidence, id, e.Kind)
		case e.State != memory.StateActive && e.State != "":
			return Packet{}, fmt.Errorf("%w: required contract %s is %s", workflow.ErrIncompleteEvidence, id, e.State)
		}
		if have[id] {
			for j := range optional {
				if optional[j].e.ID == id {
					required = append(required, shown{optional[j].e, optional[j].reason, true})
					optional = append(optional[:j], optional[j+1:]...)
					break
				}
			}
			for j := range required {
				if required[j].e.ID == id {
					required[j].reason = "required_contract"
				}
			}
			continue
		}
		required = append(required, shown{e, "required_contract", true})
	}

	// Mandatory sections, in prompt order.
	task := req.Task
	header := paragraph(req.Header)
	refs := fmt.Sprintf("Context: run %s, attempt %s, plan %s, start snapshot %s.\n\n", req.RunID, req.AttemptID, req.PlanHash[:12], req.StartSnapshotHash[:12])
	var constraints string
	if strings.TrimSpace(task.Constraints) != "" {
		constraints = "Constraints that apply to the whole plan (mandatory):\n\n" + paragraph(task.Constraints)
	}
	var contracts string
	if len(required) > 0 {
		var b strings.Builder
		b.WriteString("Decisions and contracts you must honour (mandatory):\n\n")
		for _, r := range required {
			b.WriteString(block(r.e))
		}
		b.WriteString("\n")
		contracts = b.String()
	}
	brief := "Your task:\n\n" + paragraph(task.Brief)
	footer := strings.TrimRight(req.Footer, "\n") + "\n"
	mandatory := []Section{{"header", len(header)}, {"refs", len(refs)}, {"constraints", len(constraints)}, {"contracts", len(contracts)}, {"brief", len(brief)}, {"footer", len(footer)}}
	need := 0
	for _, s := range mandatory {
		need += s.Bytes
	}
	if need > limit {
		var nonEmpty []Section
		for _, s := range mandatory {
			if s.Bytes > 0 {
				nonEmpty = append(nonEmpty, s)
			}
		}
		return Packet{}, &TooLargeError{Limit: limit, Required: need, Sections: nonEmpty}
	}

	// Optional facts: all of them if they all fit; otherwise, in order, each one
	// that fits whole in what is left after the notice that will follow them.
	const optionalHeading = "Facts from earlier work (optional context; a claim is not verified):\n\n"
	blocks := make([]string, len(optional))
	all := 0
	for i, o := range optional {
		blocks[i] = block(o.e)
		all += len(blocks[i])
	}
	var included []int
	var omitted []memory.Item
	notice := ""
	factsBytes := 0
	switch {
	case len(optional) == 0:
	case need+len(optionalHeading)+all+1 <= limit:
		for i := range optional {
			included = append(included, i)
		}
		factsBytes = all
	default:
		left := limit - need - len(optionalHeading) - 1 - len(omissionNotice)
		for i := range optional {
			if len(blocks[i]) <= left {
				included = append(included, i)
				left -= len(blocks[i])
				factsBytes += len(blocks[i])
			} else {
				omitted = append(omitted, memory.Item{ID: optional[i].e.ID, Reason: "over_budget"})
			}
		}
		if len(omitted) > 0 {
			notice = omissionNotice
		}
	}
	var facts string
	if len(included) > 0 {
		var b strings.Builder
		b.WriteString(optionalHeading)
		for _, i := range included {
			b.WriteString(blocks[i])
		}
		b.WriteString("\n")
		facts = b.String()
	}

	prompt := header + refs + constraints + contracts + brief + facts + notice + footer
	optionalBytes := len(facts) + len(notice)
	if len(prompt) > limit || len(prompt) != need+optionalBytes {
		return Packet{}, fmt.Errorf("task context: accounting error: %d bytes rendered, %d counted, limit %d", len(prompt), need+optionalBytes, limit)
	}

	// Entries that applied but were left out for a reason of their own.
	for _, it := range explained.Excluded {
		if it.Reason != memory.ReasonNotApplicable {
			omitted = append(omitted, memory.Item{ID: it.ID, Reason: it.Reason})
		}
	}
	sort.Slice(omitted, func(i, j int) bool { return omitted[i].ID < omitted[j].ID })

	m := manifest{SchemaVersion: schemaVersion, RunID: req.RunID, AttemptID: req.AttemptID, PlanHash: req.PlanHash, StartSnapshot: req.StartSnapshotHash,
		TaskID: task.ID, Method: EstimateMethod, Limit: limit, Selected: []selection{}, Omitted: []omission{}}
	m.Estimated.Required, m.Estimated.Optional, m.Estimated.Total = need, optionalBytes, len(prompt)
	m.Sections = append(m.Sections, mandatory...)
	m.Sections = append(m.Sections, Section{"facts", len(facts)}, Section{"notice", len(notice)})
	for _, r := range required {
		m.Selected = append(m.Selected, selection{r.e.ID, r.reason, true})
	}
	for _, i := range included {
		m.Selected = append(m.Selected, selection{optional[i].e.ID, optional[i].reason, false})
	}
	for _, o := range omitted {
		m.Omitted = append(m.Omitted, omission{o.ID, o.Reason})
	}
	sum := sha256.Sum256([]byte(prompt))
	m.PromptHash = hex.EncodeToString(sum[:])
	cfg, _ := json.Marshal(struct {
		Schema   int    `json:"schema_version"`
		Method   string `json:"estimate_method"`
		Limit    int    `json:"limit"`
		Template string `json:"template"`
	}{schemaVersion, EstimateMethod, limit, templateVersion})
	cs := sha256.Sum256(cfg)
	m.ConfigHash = hex.EncodeToString(cs[:])
	raw, err := json.Marshal(m)
	if err != nil {
		return Packet{}, err
	}
	return Packet{Prompt: prompt, Manifest: raw, EstimatedTokens: len(prompt), EstimateMethod: EstimateMethod}, nil
}

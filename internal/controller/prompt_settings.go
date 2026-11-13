package controller

import (
	"fmt"
	"slices"
	"strings"

	"github.com/emsnoel/tandem/internal/agent"
)

// promptSettings is what every task prompt of a run carries from the project
// and the handoff. The wording is the legacy engine's, kept as it was.
type promptSettings struct {
	// Conventions is the conventions file to ask the builder to read, relative
	// to the root. It is empty when the builder's CLI loads that file itself.
	Conventions string `json:"conventions,omitempty"`
	PromptExtra string `json:"prompt_extra,omitempty"`
	Ruling      string `json:"ruling,omitempty"`
	// Objection is the start of the review the ruling overrules, so the builder
	// knows which steps the ruling covers.
	Objection string `json:"objection,omitempty"`
}

// settingsFor is the run's settings, or nil when it has none.
func settingsFor(req StartRequest) *promptSettings {
	s := promptSettings{PromptExtra: req.PromptExtra, Ruling: req.Ruling}
	if s.Ruling != "" {
		s.Objection = req.RulingObjection
	}
	if f := req.ConventionsFile; f != "" && !loadsItself(req.BuilderKind, f) {
		s.Conventions = f
	}
	if s == (promptSettings{}) {
		return nil
	}
	return &s
}

// loadsItself says whether the builder's CLI loads file into every
// conversation, so asking for it again would only cost tokens.
func loadsItself(kind, file string) bool {
	a, err := agent.Get(kind)
	return err == nil && slices.Contains(a.InstructionFiles(), file)
}

// prelude follows the line naming the task.
func (s *promptSettings) prelude(root string) string {
	if s == nil || s.Conventions == "" {
		return ""
	}
	return fmt.Sprintf("\n\nAlso read %s/%s in full before you start. It carries\nconstraints you cannot infer from the code.", root, s.Conventions)
}

// closing comes before the acknowledge and hand-back instructions. The ruling
// is the one exception to "the plan wins", and prompt_extra comes last so the
// repo's own text has the final word.
func (s *promptSettings) closing() string {
	if s == nil {
		return ""
	}
	var b strings.Builder
	if s.Ruling != "" {
		b.WriteString("OVERRULED OBJECTIONS\nYou raised these against this plan and the planner has ruled. These rulings\n")
		b.WriteString("outrank the plan - they are the only part of this message that does. Do not\nraise them again.\n\n")
		for _, line := range strings.Split(strings.TrimRight(s.Objection, "\n"), "\n") {
			b.WriteString("  " + line + "\n")
		}
		fmt.Fprintf(&b, "\n  RULING: %s\n\n", s.Ruling)
	}
	if s.PromptExtra != "" {
		b.WriteString(strings.TrimRight(s.PromptExtra, "\n") + "\n\n")
	}
	return b.String()
}

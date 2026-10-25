package decision

import (
	"fmt"
	"strings"
)

// Render turns an advisory into the block for the handoff prompt's EXTRA slot,
// or "". effortExpressible comes from Adapter.Supports("subagent_effort"); when
// false, no floor is named, but the structural advice still applies. The
// wording is permissive so it never outranks the reviewed plan or a ruling.
func (a Advisory) Render(effortExpressible bool) string {
	if a.Outcome == OutcomeNoAdvice {
		return ""
	}

	var b strings.Builder
	b.WriteString("<staffing-advice source=\"tandem\" contract=\"" + ContractVersion + "\">\n")
	b.WriteString("Advisory only. You may disregard any of this.\n\n")

	switch a.Outcome {
	case OutcomeDelegate:
		b.WriteString(fmt.Sprintf("This plan reads as %s, mostly routine work.\n", a.Shape))
		if a.Parallel {
			b.WriteString("Its parts look independent and could proceed in parallel.\n")
		}
		if effortExpressible {
			b.WriteString(fmt.Sprintf("If you delegate it, %s is the lowest level to use.\n", a.Floor))
		}
	case OutcomeDontDelegate:
		b.WriteString(fmt.Sprintf("This plan reads as %s. It does not look like\n", a.Shape))
		b.WriteString("routine execution, so delegating it cheaply is not advised.\n")
		if effortExpressible {
			b.WriteString(fmt.Sprintf("If you delegate any of it anyway, stay at %s or above.\n", a.Floor))
		}
	}

	if len(a.Reasons) > 0 {
		b.WriteString("\nWhy: " + strings.Join(a.Reasons, "; ") + ".\n")
	}
	b.WriteString("</staffing-advice>\n")
	return b.String()
}

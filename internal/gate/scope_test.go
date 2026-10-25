package gate

import (
	"strings"
	"testing"
)

// tenLines is the stand-in for a plan: ten distinct lines.
func tenLines() string {
	var b strings.Builder
	for i := 1; i <= 10; i++ {
		b.WriteString("line ")
		b.WriteByte(byte('0' + i%10))
		b.WriteByte('\n')
	}
	return b.String()
}

// editLines rewrites the first n lines of s.
func editLines(s string, n int) string {
	ls := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	for i := 0; i < n; i++ {
		ls[i] = "edited " + ls[i]
	}
	return strings.Join(ls, "\n") + "\n"
}

func TestChangedLines(t *testing.T) {
	p := tenLines()
	for _, c := range []struct {
		name string
		a, b string
		want int
	}{
		{"identical", p, p, 0},
		{"one line edited counts removed and added", p, editLines(p, 1), 2},
		{"one line appended", p, p + "extra\n", 1},
		{"from nothing", "", "a\nb\nc\n", 3},
		{"missing final newline is not a change", "a\nb", "a\nb\n", 0},
	} {
		if got := ChangedLines(c.a, c.b); got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
}

func TestReviewScope(t *testing.T) {
	edited := func(m func(*ReviewInput)) ReviewInput {
		in := clean()
		in.PlanReviewed = tenLines()
		in.PlanNow = editLines(tenLines(), 1)
		m(&in)
		return in
	}
	for _, c := range []struct {
		name string
		in   ReviewInput
		want string
	}{
		{"a small edit since the last review", edited(func(*ReviewInput) {}), "diff"},
		{"half the plan rewritten is still a diff", edited(func(i *ReviewInput) { i.PlanNow = editLines(tenLines(), 5) }), "diff"},
		{"more than half rewritten reads the plan", edited(func(i *ReviewInput) { i.PlanNow = editLines(tenLines(), 6) }), "full"},
		{"never reviewed", edited(func(i *ReviewInput) { i.Found = false }), "full"},
		{"plan texts unread", edited(func(i *ReviewInput) { i.PlanMeasured = false }), "full"},
		{"unchanged plan asked again", edited(func(i *ReviewInput) { i.PlanNow = i.PlanReviewed }), "full"},
		{"builder kind changed", edited(func(i *ReviewInput) { i.KindNow = "claude" }), "full"},
		{"builder autonomy changed", edited(func(i *ReviewInput) { i.AutonomyNow = "ask" }), "full"},
		{"identity unread", edited(func(i *ReviewInput) { i.IdentityMeasured = false }), "full"},
		{"previous review written by the builder", edited(func(i *ReviewInput) { i.ReviewedBy = "builder" }), "full"},
		{"previous review from before reviewed_by existed", edited(func(i *ReviewInput) { i.ReviewedBy = "" }), "full"},
		{"previous review by another reviewer kind", edited(func(i *ReviewInput) { i.ReviewerKindNow = "codex" }), "full"},
		{"previous review by another measured model", edited(func(i *ReviewInput) { i.ReviewerModelNow = "sonnet" }), "full"},
		{"another model, unmeasured, same kind", edited(func(i *ReviewInput) {
			i.ReviewerModelNow, i.ReviewerModelMeasuredNow = "sonnet", false
		}), "diff"},
		{"no reviewer recorded", edited(func(i *ReviewInput) { i.ReviewerRecorded = false }), "full"},
		{"empty plan now", edited(func(i *ReviewInput) { i.PlanNow = "" }), "full"},
	} {
		if got := ReviewScope(c.in); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

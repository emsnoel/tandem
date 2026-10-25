package snapshot

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/emsnoel/tandem/internal/artifact"
	"github.com/emsnoel/tandem/internal/workflow"
)

// maxInlineBytes bounds a file whose text is shown in a rendered patch.
// Larger files, and binary ones, appear as references to their stored
// before and after content.
const maxInlineBytes = 1 << 20

// RenderPatch stores a readable unified-style patch for p as an artifact and
// returns its hash. Text changes show their lines; a binary or oversized file
// shows only its before and after content hashes, which name stored artifacts.
// An incomplete patch is refused: there is no honest text for it.
func RenderPatch(ctx context.Context, store *artifact.Store, p Patch) (string, error) {
	if !p.Complete {
		return "", fmt.Errorf("%w: the patch is incomplete: %s", workflow.ErrIncompleteEvidence, strings.Join(p.Errors, "; "))
	}
	var out bytes.Buffer
	for _, c := range p.Changes {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		name := c.PathString()
		if c.PathB64 != "" {
			name = "path_b64:" + c.PathB64
		}
		fmt.Fprintf(&out, "diff --tandem a/%s b/%s\n", name, name)
		switch c.Op {
		case OpAdd:
			fmt.Fprintf(&out, "new file mode %s\n", mode(c.AfterKind, c.AfterExecutable))
		case OpDelete:
			fmt.Fprintf(&out, "deleted file mode %s\n", mode(c.BeforeKind, c.BeforeExecutable))
		default:
			if c.BeforeExecutable != c.AfterExecutable || c.BeforeKind != c.AfterKind {
				fmt.Fprintf(&out, "old mode %s\nnew mode %s\n", mode(c.BeforeKind, c.BeforeExecutable), mode(c.AfterKind, c.AfterExecutable))
			}
		}
		if c.Op == OpMode {
			continue
		}
		if err := renderBody(ctx, store, &out, name, c); err != nil {
			return "", err
		}
	}
	return store.Put(ctx, bytes.NewReader(out.Bytes()))
}

func mode(kind string, exec bool) string {
	switch {
	case kind == KindSymlink:
		return "120000"
	case kind == KindGitlink:
		return "160000"
	case exec:
		return "100755"
	}
	return "100644"
}

func renderBody(ctx context.Context, store *artifact.Store, out *bytes.Buffer, name string, c Change) error {
	if c.BeforeKind == KindSymlink || c.AfterKind == KindSymlink || c.BeforeKind == KindGitlink || c.AfterKind == KindGitlink {
		fmt.Fprintf(out, "%s change: before %s %s, after %s %s\n", symOrGit(c), orNone(c.BeforeKind), orNone(c.BeforeHash), orNone(c.AfterKind), orNone(c.AfterHash))
		if c.AfterKind == KindSymlink {
			if t, err := store.ReadAll(ctx, c.AfterHash, 4096); err == nil {
				fmt.Fprintf(out, "+symlink -> %s\n", t)
			}
		}
		return nil
	}
	var before, after []byte
	var err error
	if c.BeforeHash != "" {
		if c.BeforeSize > maxInlineBytes {
			return refs(out, c, "too large to show")
		}
		if before, err = store.ReadAll(ctx, c.BeforeHash, maxInlineBytes); err != nil {
			return err
		}
	}
	if c.AfterHash != "" {
		if c.AfterSize > maxInlineBytes {
			return refs(out, c, "too large to show")
		}
		if after, err = store.ReadAll(ctx, c.AfterHash, maxInlineBytes); err != nil {
			return err
		}
	}
	if isBinary(before) || isBinary(after) {
		return refs(out, c, "Binary files differ")
	}
	fmt.Fprintf(out, "--- %s\n+++ %s\n", side("a/"+name, c.Op == OpAdd), side("b/"+name, c.Op == OpDelete))
	out.WriteString(unified(before, after))
	return nil
}

func refs(out *bytes.Buffer, c Change, why string) error {
	fmt.Fprintf(out, "%s: before %s after %s\n", why, orNone(c.BeforeHash), orNone(c.AfterHash))
	return nil
}

func side(name string, absent bool) string {
	if absent {
		return "/dev/null"
	}
	return name
}

func orNone(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func symOrGit(c Change) string {
	if c.BeforeKind == KindGitlink || c.AfterKind == KindGitlink {
		return "gitlink"
	}
	return "symlink"
}

// isBinary applies git's rule: a NUL byte in the first 8000 bytes.
func isBinary(b []byte) bool {
	if len(b) > 8000 {
		b = b[:8000]
	}
	return bytes.IndexByte(b, 0) >= 0
}

// unified returns hunks with three lines of context from an LCS line diff. A
// file pair too large for the table is shown as a whole replacement.
func unified(before, after []byte) string {
	a, b := lines(before), lines(after)
	type op struct {
		kind byte // ' ', '-', '+'
		text string
	}
	var ops []op
	if len(a)*len(b) > 4_000_000 {
		for _, l := range a {
			ops = append(ops, op{'-', l})
		}
		for _, l := range b {
			ops = append(ops, op{'+', l})
		}
	} else {
		lcs := make([][]int32, len(a)+1)
		for i := range lcs {
			lcs[i] = make([]int32, len(b)+1)
		}
		for i := len(a) - 1; i >= 0; i-- {
			for j := len(b) - 1; j >= 0; j-- {
				if a[i] == b[j] {
					lcs[i][j] = lcs[i+1][j+1] + 1
				} else {
					lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
				}
			}
		}
		i, j := 0, 0
		for i < len(a) || j < len(b) {
			switch {
			case i < len(a) && j < len(b) && a[i] == b[j]:
				ops = append(ops, op{' ', a[i]})
				i, j = i+1, j+1
			case j < len(b) && (i == len(a) || lcs[i][j+1] >= lcs[i+1][j]):
				ops = append(ops, op{'+', b[j]})
				j++
			default:
				ops = append(ops, op{'-', a[i]})
				i++
			}
		}
	}

	const context = 3
	var out strings.Builder
	n := len(ops)
	for start := 0; start < n; {
		for start < n && ops[start].kind == ' ' {
			start++
		}
		if start >= n {
			break
		}
		lo := max(start-context, 0)
		hi := start
		// Extend the hunk while another change is within twice the context.
		for hi < n {
			next := hi
			for next < n && ops[next].kind == ' ' {
				next++
			}
			if next >= n || next-hi > 2*context {
				break
			}
			for next < n && ops[next].kind != ' ' {
				next++
			}
			hi = next
		}
		end := min(hi+context, n)
		var aStart, bStart, aCount, bCount int
		for k := 0; k < lo; k++ {
			if ops[k].kind != '+' {
				aStart++
			}
			if ops[k].kind != '-' {
				bStart++
			}
		}
		for k := lo; k < end; k++ {
			if ops[k].kind != '+' {
				aCount++
			}
			if ops[k].kind != '-' {
				bCount++
			}
		}
		fmt.Fprintf(&out, "@@ -%d,%d +%d,%d @@\n", aStart+1, aCount, bStart+1, bCount)
		for k := lo; k < end; k++ {
			out.WriteByte(ops[k].kind)
			out.WriteString(ops[k].text)
			if !strings.HasSuffix(ops[k].text, "\n") {
				out.WriteString("\n\\ No newline at end of file\n")
			}
		}
		start = end
	}
	return out.String()
}

func lines(b []byte) []string {
	if len(b) == 0 {
		return nil
	}
	var out []string
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			out = append(out, string(b))
			break
		}
		out = append(out, string(b[:i+1]))
		b = b[i+1:]
	}
	return out
}

package check

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/emsnoel/tandem/internal/evidence"
)

// MaxSummaryBytes bounds a summary shown to a model, including its log
// reference and any truncation marker.
const MaxSummaryBytes = 8192

const (
	// A line longer than head+tail keeps only its start and end.
	lineHead = 300
	lineTail = 100
	maxRef   = 512
)

// SummarizeFile is Summary for a log too large, or with lines too long, to read
// into memory. It streams the log once, keeping the first maxMatches failing
// lines and the last few lines, each cut to its head and tail with the omitted
// size stated. The result is at most maxBytes, including the truncation marker
// and ref (a path or artifact hash).
func SummarizeFile(r Result, ref string, maxBytes int) (string, error) {
	f, err := os.Open(r.Log)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if len(ref) > maxRef {
		ref = ref[:maxRef]
	}

	tailKeep := tailLines
	if r.Exit == 0 {
		tailKeep = 3
	}
	type kept struct {
		n    int
		text string
	}
	var hits []kept
	tail := make([]kept, 0, tailKeep+1)
	total := 0

	br := bufio.NewReaderSize(f, 64*1024)
	var head []byte
	var ring []byte // the last lineTail bytes of the current line
	var omitted int64
	flush := func() {
		text := string(head)
		if omitted > 0 {
			text += fmt.Sprintf("…[%d bytes omitted]…", omitted) + string(ring)
		}
		total++
		// Redact before measuring, so redaction cannot push the summary past its bound.
		l := kept{total, evidence.Redact(strings.ToValidUTF8(text, "�"))}
		if r.Exit != 0 && len(hits) < maxMatches && failRE.MatchString(l.text) {
			hits = append(hits, l)
		}
		tail = append(tail, l)
		if len(tail) > tailKeep {
			tail = tail[1:]
		}
		head, ring, omitted = head[:0], ring[:0], 0
	}
	pending := false
	for {
		chunk, isPrefix, err := br.ReadLine()
		if len(chunk) > 0 || err == nil {
			pending = true
			room := lineHead - len(head)
			if room > 0 {
				n := min(room, len(chunk))
				head = append(head, chunk[:n]...)
				chunk = chunk[n:]
			}
			if len(chunk) > 0 {
				// Past the head: remember only the last lineTail bytes.
				ring = append(ring, chunk...)
				if len(ring) > lineTail {
					omitted += int64(len(ring) - lineTail)
					ring = append(ring[:0], ring[len(ring)-lineTail:]...)
				}
			}
		}
		if err != nil {
			break
		}
		if !isPrefix {
			flush()
			pending = false
		}
	}
	if pending {
		flush()
	}

	status := fmt.Sprintf("exit %d", r.Exit)
	if r.TimedOut {
		status = "timed out"
	}
	// A hit inside the tail would be shown twice.
	firstTail := total + 1
	if len(tail) > 0 {
		firstTail = tail[0].n
	}
	var failing []string
	for _, h := range hits {
		if h.n < firstTail {
			failing = append(failing, h.text)
		}
	}
	var last []string
	for _, l := range tail {
		last = append(last, l.text)
	}
	return fit(fmt.Sprintf("check: %s in %s\n", status, r.Duration.Round(1e9)), ref, failing, last, maxBytes), nil
}

// fit renders the sections and sheds content until the whole fits maxBytes,
// then guarantees it with a hard cut and a marker.
func fit(header, ref string, failing, last []string, maxBytes int) string {
	marker := fmt.Sprintf("\n[summary truncated; full log: %s]\n", ref)
	render := func() string {
		var b strings.Builder
		b.WriteString(header)
		fmt.Fprintf(&b, "log: %s\n", ref)
		if len(failing) > 0 {
			b.WriteString("failing lines:\n")
			for _, l := range failing {
				b.WriteString("  " + l + "\n")
			}
		}
		if len(last) > 0 {
			b.WriteString("last lines:\n")
			for _, l := range last {
				b.WriteString("  " + l + "\n")
			}
		}
		return b.String()
	}
	out := render()
	shed := false
	for len(out) > maxBytes && (len(failing) > 0 || len(last) > 1) {
		shed = true
		if len(failing) > 0 {
			failing = failing[:len(failing)-1]
		} else {
			last = last[1:]
		}
		out = render()
	}
	if len(out) <= maxBytes && !shed {
		return out
	}
	if len(out)+len(marker) <= maxBytes {
		return out + marker
	}
	cut := max(maxBytes-len(marker), 0)
	for cut > 0 && !utf8.RuneStart(out[cut]) {
		cut--
	}
	if cut > len(out) {
		cut = len(out)
	}
	res := out[:cut] + marker
	if len(res) > maxBytes {
		res = res[:maxBytes]
	}
	return res
}

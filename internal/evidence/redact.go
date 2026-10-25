package evidence

import "regexp"

// The redaction set is small and specific. A broad pattern that eats ordinary
// prose degrades every answer; redaction is a second line behind not sending
// diffs, history or the environment at all.
var redactions = []struct {
	re   *regexp.Regexp
	with string
}{
	// KEY=value in an export or assignment. The NAME is left visible: it is
	// useful context and is not itself a secret.
	{regexp.MustCompile(`(?i)\b([A-Z0-9_]*(?:KEY|TOKEN|SECRET|PASSWORD|PASSWD)[A-Z0-9_]*)\s*=\s*\S+`), "$1=[REDACTED]"},
	// Authorization headers.
	{regexp.MustCompile(`(?i)(Bearer|Basic)\s+[A-Za-z0-9._~+/=-]{8,}`), "$1 [REDACTED]"},
	// PEM blocks, body only.
	{regexp.MustCompile(`(?s)(-----BEGIN [A-Z ]*PRIVATE KEY-----).*?(-----END [A-Z ]*PRIVATE KEY-----)`), "$1[REDACTED]$2"},
}

// Redact removes common secret shapes from text bound for a remote service.
func Redact(s string) string {
	for _, r := range redactions {
		s = r.re.ReplaceAllString(s, r.with)
	}
	return s
}

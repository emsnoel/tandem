package evidence

import (
	"strings"
	"testing"
)

func TestRedactsExportedSecrets(t *testing.T) {
	in := "run this first:\nexport TYPESAFE_API_KEY=ts-live-abc123def456\nthen build"
	got := Redact(in)
	if strings.Contains(got, "ts-live-abc123def456") {
		t.Fatalf("the secret value survived redaction: %q", got)
	}
	if !strings.Contains(got, "TYPESAFE_API_KEY") {
		t.Fatalf("the variable NAME is useful context and should survive: %q", got)
	}
}

func TestRedactsBearerTokens(t *testing.T) {
	got := Redact("curl -H 'Authorization: Bearer sk-abcdef123456789'")
	if strings.Contains(got, "sk-abcdef123456789") {
		t.Fatalf("the bearer token survived: %q", got)
	}
}

func TestRedactsPrivateKeyBlocks(t *testing.T) {
	in := "-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA\n-----END RSA PRIVATE KEY-----"
	got := Redact(in)
	if strings.Contains(got, "MIIEowIBAAKCAQEA") {
		t.Fatalf("private key material survived: %q", got)
	}
}

// Redaction must not eat ordinary prose. A plan is mostly English and code.
func TestRedactLeavesOrdinaryPlanTextAlone(t *testing.T) {
	in := "## Task 1\nIn `auth/hash.go`, change the cost constant from 10 to 12.\nRun `go test ./auth/...`."
	if got := Redact(in); got != in {
		t.Fatalf("ordinary plan text was modified:\nwant %q\ngot  %q", in, got)
	}
}

// The redactor runs inside Build, so a caller cannot forget it.
func TestBuildRedacts(t *testing.T) {
	s := Build("export AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI7MDENGbPxRfiCY",
		true, "claude", "auto", "contract/v1", "policy/v1", "jev-1.13.0")
	if strings.Contains(s.Plan, "wJalrXUtnFEMI7MDENGbPxRfiCY") {
		t.Fatalf("Build must redact before it stores: %q", s.Plan)
	}
}

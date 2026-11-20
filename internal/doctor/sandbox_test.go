package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// gradleRepo is a Gradle repository with a codex builder and reviewer, as in
// emsnoel/tandem#85.
const gradleRepo = `{"check_command": "./gradlew check --console=plain",
	"builder": {"kind": "codex"}, "reviewer": {"kind": "codex"}}`

func userConfig(t *testing.T, body string) {
	t.Helper()
	if err := os.WriteFile(os.Getenv("HERDR_TANDEM_CONFIG"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func gradleHome(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(os.Getenv("HOME"), ".gradle")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

// Without a grant, the codex reviewer's sandbox blocks Gradle, and doctor says
// so before a review shows it in a verdict.
func TestDoctorWarnsTheSandboxBlocksGradle(t *testing.T) {
	root := isolate(t, gradleRepo)
	dir := gradleHome(t)
	rs := repoResults(t, root, RoleFlags{})
	for _, role := range []string{"builder", "reviewer"} {
		r := find(t, rs, "sandbox:"+role)
		if r.Status != Warn || !strings.Contains(r.Detail, "blocks gradle") {
			t.Errorf("%s: got %+v, want a warning that the sandbox blocks gradle", role, r)
		}
		want := `"sandbox": {"writable_dirs": ["` + dir + `"], "network": true} under adapters.codex`
		if !strings.Contains(r.Fix, want) {
			t.Errorf("%s: fix %q does not name %s", role, r.Fix, want)
		}
	}
}

// With the grant, the same repository passes.
func TestDoctorPassesAGrantedGradleSandbox(t *testing.T) {
	root := isolate(t, gradleRepo)
	dir := gradleHome(t)
	userConfig(t, `{"adapters":{"codex":{"sandbox":{"writable_dirs":["`+dir+`"],"network":true}}}}`)
	rs := repoResults(t, root, RoleFlags{})
	for _, role := range []string{"builder", "reviewer"} {
		if r := find(t, rs, "sandbox:"+role); r.Status != OK {
			t.Errorf("%s: got %+v, want ok", role, r)
		}
	}
}

// A granted directory that does not exist cannot be created from inside the
// sandbox. GRADLE_USER_HOME moves it.
func TestDoctorWarnsAMissingGradleHome(t *testing.T) {
	root := isolate(t, `{"reviewer": {"kind": "codex"}}`)
	if err := os.WriteFile(filepath.Join(root, "settings.gradle.kts"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "gh")
	t.Setenv("GRADLE_USER_HOME", dir)
	userConfig(t, `{"adapters":{"codex":{"sandbox":{"writable_dirs":["`+dir+`"],"network":true}}}}`)
	r := find(t, repoResults(t, root, RoleFlags{}), "sandbox:reviewer")
	if r.Status != Warn || !strings.Contains(r.Detail, "does not exist") || !strings.Contains(r.Fix, "mkdir "+dir) {
		t.Fatalf("got %+v", r)
	}
}

// A read-only reviewer writes nothing, so a grant cannot help on its own.
func TestDoctorNamesAReadonlyReviewer(t *testing.T) {
	root := isolate(t, `{"check_command": "gradle test", "reviewer": {"kind": "codex", "autonomy": "readonly"}}`)
	dir := gradleHome(t)
	userConfig(t, `{"adapters":{"codex":{"sandbox":{"writable_dirs":["`+dir+`"],"network":true}}}}`)
	r := find(t, repoResults(t, root, RoleFlags{}), "sandbox:reviewer")
	if r.Status != Warn || !strings.Contains(r.Detail, "read-only") || !strings.Contains(r.Fix, "reviewer.autonomy to auto") {
		t.Fatalf("got %+v", r)
	}
}

// No Gradle, or no sandbox, means no sandbox result at all.
func TestDoctorIsQuietWithoutAKnownNeed(t *testing.T) {
	for name, manifest := range map[string]string{
		"not gradle":   `{"check_command": "go test ./...", "reviewer": {"kind": "codex"}}`,
		"full codex":   `{"check_command": "./gradlew check", "builder": {"kind": "codex", "autonomy": "full"}, "reviewer": {"kind": "codex", "autonomy": "full"}}`,
		"claude agent": `{"check_command": "./gradlew check", "builder": {"kind": "claude"}, "reviewer": {"kind": "claude"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			root := isolate(t, manifest)
			for _, r := range repoResults(t, root, RoleFlags{}) {
				if strings.HasPrefix(r.Name, "sandbox:") {
					t.Errorf("unexpected %+v", r)
				}
			}
		})
	}
}

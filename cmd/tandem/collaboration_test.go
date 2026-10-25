package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/state"
)

var hex32 = regexp.MustCompile(`^[0-9a-f]{32}$`)

func (e *cliEnv) file(name, body string) string {
	e.t.Helper()
	p := filepath.Join(e.t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		e.t.Fatal(err)
	}
	return p
}

func (e *cliEnv) collab(args ...string) (int, map[string]any, string) {
	e.t.Helper()
	rc, got, _, errOut := e.run3(append([]string{"collaboration"}, append(args, "--root", e.root, "--state-home", e.home)...)...)
	return rc, got, errOut
}

func (e *cliEnv) createCollaboration() string {
	e.t.Helper()
	rc, got, errOut := e.collab("create", "--policy", e.file("policy.json", `{"input_ceilings":{"codex":50000},"output_ceilings":{"codex":5000},"note":"kept"}`))
	if rc != exitcode.OK {
		e.t.Fatalf("create: %d %s", rc, errOut)
	}
	id, _ := got["collaboration_id"].(string)
	if !hex32.MatchString(id) {
		e.t.Fatalf("collaboration id %v", got)
	}
	return id
}

func (e *cliEnv) stateDB() *sql.DB {
	e.t.Helper()
	id, bound, err := state.LookupIdentity(e.root, e.home)
	if err != nil || !bound {
		e.t.Fatalf("identity %v %v", bound, err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(id.StateRoot, "state.sqlite")+"?mode=ro")
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { db.Close() })
	return db
}

func TestCollaborationCreatePrintsADurableID(t *testing.T) {
	e := newCLI(t)
	id := e.createCollaboration()
	var policy, status string
	var ceiling int64
	db := e.stateDB()
	if err := db.QueryRow(`SELECT policy_hash,status FROM collaborations WHERE id=?`, id).Scan(&policy, &status); err != nil || status != "open" {
		t.Fatalf("stored collaboration %q %v", status, err)
	}
	if err := db.QueryRow(`SELECT input_ceiling FROM provider_budgets WHERE collaboration_id=? AND provider='codex'`, id).Scan(&ceiling); err != nil || ceiling != 50000 {
		t.Fatalf("ceiling %d %v", ceiling, err)
	}
	// An explicit id makes the command repeatable.
	path := e.file("p.json", `{"input_ceilings":{"claude":1}}`)
	fixed := "000000000000000000000000000000aa"
	for i := 0; i < 2; i++ {
		if rc, got, errOut := e.collab("create", "--policy", path, "--id", fixed); rc != exitcode.OK || got["collaboration_id"] != fixed {
			t.Fatalf("create with --id (%d): %d %v %s", i, rc, got, errOut)
		}
	}
	other := e.file("q.json", `{"input_ceilings":{"claude":2}}`)
	if rc, _, _ := e.collab("create", "--policy", other, "--id", fixed); rc == exitcode.OK {
		t.Fatal("a different policy under an existing id was accepted")
	}
}

func TestCollaborationCreateReadsEnforcement(t *testing.T) {
	e := newCLI(t)
	rc, got, errOut := e.collab("create", "--policy", e.file("enforced.json", `{"enforce_budgets":true,"unlimited_providers":["claude"],
		"input_ceilings":{"codex":900},"output_ceilings":{"codex":90},"input_reservations":{"codex":300},"output_reservations":{"codex":30}}`))
	if rc != exitcode.OK {
		t.Fatalf("create: %d %s", rc, errOut)
	}
	id := got["collaboration_id"].(string)
	var enforced, unlimited int
	var reservation int64
	db := e.stateDB()
	if err := db.QueryRow(`SELECT budget_enforced FROM collaborations WHERE id=?`, id).Scan(&enforced); err != nil || enforced != 1 {
		t.Fatalf("enforced %d %v", enforced, err)
	}
	if err := db.QueryRow(`SELECT unlimited FROM provider_budgets WHERE collaboration_id=? AND provider='claude'`, id).Scan(&unlimited); err != nil || unlimited != 1 {
		t.Fatalf("claude unlimited %d %v", unlimited, err)
	}
	if err := db.QueryRow(`SELECT input_reservation FROM provider_budgets WHERE collaboration_id=? AND provider='codex'`, id).Scan(&reservation); err != nil || reservation != 300 {
		t.Fatalf("codex reservation %d %v", reservation, err)
	}
}

func TestCollaborationCreateRejectsBadPolicies(t *testing.T) {
	e := newCLI(t)
	for name, body := range map[string]string{
		"not json":         `{`,
		"not an object":    `[]`,
		"negative ceiling": `{"input_ceilings":{"codex":-1}}`,
		"fraction":         `{"output_ceilings":{"codex":1.5}}`,
		"ceilings list":    `{"input_ceilings":[1]}`,
		"non-finite":       `{"input_ceilings":{"codex":NaN}}`,
		"enforce not bool": `{"enforce_budgets":"yes"}`,
		"unlimited names":  `{"unlimited_providers":[1]}`,
		"unlimited capped": `{"unlimited_providers":["codex"],"input_ceilings":{"codex":5}}`,
		"bad reservation":  `{"input_reservations":{"codex":-5}}`,
	} {
		if rc, _, errOut := e.collab("create", "--policy", e.file("bad.json", body)); rc != exitcode.Internal {
			t.Errorf("%s: rc %d %s", name, rc, errOut)
		}
	}
	if rc, _, _ := e.collab("create"); rc != exitcode.Usage {
		t.Errorf("create without --policy: %d", rc)
	}
	if rc, _, _ := e.collab("create", "--policy", e.file("p.json", `{}`), "--id", "nope"); rc != exitcode.Usage {
		t.Errorf("a malformed --id: %d", rc)
	}
}

func TestCollaborationRegisterSourceValidatesItsJSON(t *testing.T) {
	e := newCLI(t)
	id := e.createCollaboration()
	planner := e.file("planner.json", `{"provider":"claude","model":"claude-a","role":"planner","session_id":"planner-1","start_boundary":"msg-9"}`)
	rc, got, errOut := e.collab("register-source", "--collaboration", id, "--source", planner)
	src, _ := got["source_id"].(string)
	if rc != exitcode.OK || !hex32.MatchString(src) {
		t.Fatalf("register: %d %v %s", rc, got, errOut)
	}
	var role, session, owner string
	if err := e.stateDB().QueryRow(`SELECT role,session_id,collaboration_id FROM expected_sources WHERE id=?`, src).Scan(&role, &session, &owner); err != nil ||
		role != "planner" || session != "planner-1" || owner != id {
		t.Fatalf("stored source %s/%s/%s %v", role, session, owner, err)
	}
	for name, body := range map[string]string{
		"unknown field":         `{"provider":"claude","role":"builder","owner":"someone"}`,
		"no role":               `{"provider":"claude"}`,
		"planner no session":    `{"provider":"claude","role":"planner"}`,
		"names a collaboration": `{"collaboration_id":"x","provider":"codex","role":"builder"}`,
		"not json":              `{"provider":`,
	} {
		if rc, _, errOut := e.collab("register-source", "--collaboration", id, "--source", e.file("s.json", body)); rc != exitcode.Internal {
			t.Errorf("%s: rc %d %s", name, rc, errOut)
		}
	}
	builder := e.file("b.json", `{"provider":"codex","role":"builder"}`)
	if rc, _, _ := e.collab("register-source", "--collaboration", "000000000000000000000000000000bb", "--source", builder); rc != exitcode.Internal {
		t.Errorf("an unknown collaboration: %d", rc)
	}
	// The same planner session cannot be owned twice at once.
	second := e.file("planner2.json", `{"provider":"claude","role":"planner","session_id":"planner-1"}`)
	if rc, _, errOut := e.collab("register-source", "--collaboration", id, "--source", second); rc != exitcode.Internal {
		t.Errorf("a second owner of an owned planner session: %d %s", rc, errOut)
	}
}

func TestCollaborationRunRegistersItsBuilderBeforeSending(t *testing.T) {
	e := newCLI(t)
	t.Setenv("TANDEM_BUILD_ENGINE", "durable")
	id := e.createCollaboration()
	if rc, _, errOut := e.start("--collaboration", id); rc != exitcode.OK {
		t.Fatalf("start: %d %s", rc, errOut)
	}
	e.sub("tick")
	var n int
	var session string
	if err := e.stateDB().QueryRow(`SELECT count(*), max(session_id) FROM expected_sources WHERE collaboration_id=? AND role='builder'`, id).Scan(&n, &session); err != nil || n != 1 {
		t.Fatalf("%d builder sources (%v)", n, err)
	}
	if session == "" {
		t.Fatal("the builder's receipt bound no session")
	}
	unknown := newCLI(t)
	if rc, _, _ := unknown.start("--collaboration", "000000000000000000000000000000cc"); rc == exitcode.OK {
		t.Fatal("a run started in an unknown collaboration")
	}
}

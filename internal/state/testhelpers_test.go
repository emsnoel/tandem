package state

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/emsnoel/tandem/internal/artifact"
)

// hexID is a deterministic 32 character lowercase hex id for fixtures.
func hexID(n int) string { return fmt.Sprintf("%032x", n) }

// seed is one checkout, project, run, task and current attempt, all with real
// artifacts behind every hash. Test-only: production inserts go through the
// controller.
type seed struct {
	s         *Store
	artifacts *artifact.Store

	CheckoutID, ProjectID, RunID, TaskID, AttemptID string
	StartHash, PacketHash, PayloadHash              string
	// Version is the attempt's initial expected version.
	Version int64
}

func (sd *seed) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := sd.s.db.ExecContext(context.Background(), q, args...); err != nil {
		t.Fatalf("seed %q: %v", q, err)
	}
}

// artifact stores content in the artifact store and records its metadata row,
// so every foreign key to artifacts(hash) points at real bytes.
func (sd *seed) artifact(t *testing.T, content string) string {
	t.Helper()
	hash, err := sd.artifacts.Put(context.Background(), bytes.NewReader([]byte(content)))
	if err != nil {
		t.Fatal(err)
	}
	sd.exec(t, `INSERT OR IGNORE INTO artifacts(hash,size_bytes,media_type,created_ms) VALUES(?,?,?,?)`,
		hash, len(content), "text/plain", 1)
	return hash
}

// seedProjectRunTaskAttempt inserts one of each, using parameterized inserts
// with foreign keys on. variant keeps the ids of a second seed distinct.
func seedProjectRunTaskAttempt(t *testing.T, s *Store, variant int) *seed {
	t.Helper()
	store, err := artifact.Open(filepath.Dir(s.path))
	if err != nil {
		t.Fatal(err)
	}
	sd := &seed{s: s, artifacts: store,
		CheckoutID: hexID(variant*10 + 1), ProjectID: hexID(variant*10 + 2), RunID: hexID(variant*10 + 3),
		AttemptID: hexID(variant*10 + 4), TaskID: "task-one"}
	plan := sd.artifact(t, fmt.Sprintf("plan %d", variant))
	config := sd.artifact(t, fmt.Sprintf("config %d", variant))
	brief := sd.artifact(t, "brief")
	constraints := sd.artifact(t, "constraints")
	spec := sd.artifact(t, "spec")
	sd.StartHash = sd.artifact(t, fmt.Sprintf("start snapshot %d", variant))
	sd.PacketHash = sd.artifact(t, fmt.Sprintf("packet %d", variant))
	sd.PayloadHash = sd.artifact(t, fmt.Sprintf("event payload %d", variant))

	sd.exec(t, `INSERT INTO checkouts(id,canonical_root,git_common_dir,created_ms) VALUES(?,?,?,?)`,
		sd.CheckoutID, fmt.Sprintf("/repo-%d", variant), fmt.Sprintf("/repo-%d/.git", variant), 1)
	sd.exec(t, `INSERT INTO projects(id,checkout_id,manifest_root) VALUES(?,?,?)`, sd.ProjectID, sd.CheckoutID, ".")
	sd.exec(t, `INSERT INTO runs(id,project_id,plan_hash,config_hash,engine_version,state,created_ms) VALUES(?,?,?,?,2,'running',1)`,
		sd.RunID, sd.ProjectID, plan, config)
	sd.exec(t, `INSERT INTO tasks(run_id,id,ordinal,brief_hash,constraints_hash,spec_hash) VALUES(?,?,1,?,?,?)`,
		sd.RunID, sd.TaskID, brief, constraints, spec)
	sd.exec(t, `INSERT INTO attempts(id,run_id,task_id,ordinal,implementation_state,check_state,review_state,budget_state,
		start_snapshot_hash,packet_hash,version,is_current,created_ms) VALUES(?,?,?,1,'pending','pending','pending','available',?,?,0,1,1)`,
		sd.AttemptID, sd.RunID, sd.TaskID, sd.StartHash, sd.PacketHash)
	return sd
}

// setAttempt sets columns directly, standing in for the controller steps
// (snapshot, patch, check and review jobs) that later tasks implement.
func (sd *seed) setAttempt(t *testing.T, set string, args ...any) {
	t.Helper()
	sd.exec(t, `UPDATE attempts SET `+set+` WHERE id=?`, append(args, sd.AttemptID)...)
}

// submitReady gives the attempt an end snapshot and patch and a running builder.
func (sd *seed) submitReady(t *testing.T) (end, patch string) {
	t.Helper()
	end = sd.artifact(t, "end snapshot")
	patch = sd.artifact(t, "patch")
	sd.setAttempt(t, `implementation_state='running', end_snapshot_hash=?, patch_hash=?, version=version+1`, end, patch)
	return end, patch
}

func (sd *seed) version(t *testing.T) int64 {
	t.Helper()
	var v int64
	if err := sd.s.db.QueryRow(`SELECT version FROM attempts WHERE id=?`, sd.AttemptID).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func (sd *seed) count(t *testing.T, table string) int {
	t.Helper()
	var n int
	if err := sd.s.db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// job inserts a completed job of kind for the attempt and returns its id.
func (sd *seed) job(t *testing.T, kind string, n int) string {
	t.Helper()
	id := hexID(1000 + n)
	sd.exec(t, `INSERT INTO jobs(id,attempt_id,kind,state,input_hash,config_hash,runner_kind) VALUES(?,?,?,'completed',?,?,'test')`,
		id, sd.AttemptID, kind, sd.PacketHash, sd.PacketHash)
	return id
}

func fixedClock() func() time.Time {
	return func() time.Time { return time.UnixMilli(5000) }
}

func artifactReader(t *testing.T, s *Store) *artifact.Store {
	t.Helper()
	a, err := artifact.Open(filepath.Dir(s.path))
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

// filepathDir is the state root of an open store.
func filepathDir(s *Store) string { return filepath.Dir(s.path) }

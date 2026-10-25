package state

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/emsnoel/tandem/internal/workflow"
)

func TestArchiveRunRetainsEverythingAndRefusesWhileWorkIsOpen(t *testing.T) {
	for name, tc := range map[string]struct {
		prep func(sd *seed, t *testing.T)
		want error
	}{
		"a finished run": {func(sd *seed, t *testing.T) { sd.exec(t, `UPDATE runs SET state='finished'`) }, nil},
		"a paused run":   {func(sd *seed, t *testing.T) { sd.exec(t, `UPDATE runs SET state='paused'`) }, nil},
		"a running run":  {func(sd *seed, t *testing.T) {}, workflow.ErrBusy},
		"a stopping run": {func(sd *seed, t *testing.T) { sd.exec(t, `UPDATE runs SET state='stopping'`) }, workflow.ErrBusy},
		"an open job": {func(sd *seed, t *testing.T) {
			sd.exec(t, `UPDATE runs SET state='paused'`)
			sd.exec(t, `INSERT INTO jobs(id,attempt_id,kind,state,input_hash,config_hash,runner_kind) VALUES(?,?,'builder','unknown',?,?,'herdr')`,
				hexID(950), sd.AttemptID, sd.StartHash, sd.PacketHash)
		}, workflow.ErrBusy},
		"an open delivery": {func(sd *seed, t *testing.T) {
			sd.exec(t, `UPDATE runs SET state='paused'`)
			sd.exec(t, `PRAGMA foreign_keys=OFF`)
			sd.exec(t, `INSERT INTO effects(id,run_id,source_event_id,kind,payload_hash,nonce,state,available_ms,created_ms) VALUES(?,?,?,'builder_dispatch',?,?,'delivery_unknown',1,1)`,
				hexID(951), sd.RunID, hexID(952), sd.StartHash, hexID(953))
			sd.exec(t, `PRAGMA foreign_keys=ON`)
		}, workflow.ErrBusy},
		"a held lease": {func(sd *seed, t *testing.T) {
			sd.exec(t, `UPDATE runs SET state='paused'`)
			if _, err := sd.s.AcquireLease(ctx, sd.CheckoutID, "someone", 1); err != nil {
				t.Fatal(err)
			}
		}, workflow.ErrBusy},
	} {
		t.Run(name, func(t *testing.T) {
			s, sd, _ := newSeededStore(t)
			var tick int64
			s.now = func() time.Time { tick++; return time.UnixMilli(1_000_000 + tick) }
			tc.prep(sd, t)
			err := s.ArchiveRun(ctx, sd.RunID)
			if !errors.Is(err, tc.want) || (tc.want == nil && err != nil) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			run, gerr := s.GetRun(ctx, sd.RunID)
			if gerr != nil {
				t.Fatal(gerr)
			}
			if tc.want != nil {
				if run.State == "archived" {
					t.Error("a refused archive changed the run")
				}
				// An open job is ended by state recover, so the refusal says so.
				if name == "an open job" && !strings.Contains(err.Error(), "tandem state recover --run "+sd.RunID) {
					t.Errorf("the open-job refusal names no remedy: %v", err)
				}
				return
			}
			if run.State != "archived" {
				t.Errorf("state %s", run.State)
			}
			var ms *int64
			s.db.QueryRowContext(ctx, `SELECT archived_ms FROM runs WHERE id=?`, sd.RunID).Scan(&ms)
			if ms == nil {
				t.Error("no archived_ms")
			}
			// Nothing is deleted: rows and artifacts stay readable.
			if as, err := s.TaskAttempts(ctx, sd.RunID, sd.TaskID); err != nil || len(as) != 1 {
				t.Errorf("attempts %v %v", as, err)
			}
			if _, err := artifactReader(t, s).Read(ctx, sd.StartHash, 0, 1); err != nil {
				t.Errorf("an artifact became unreadable: %v", err)
			}
			if err := s.ArchiveRun(ctx, sd.RunID); err != nil {
				t.Errorf("archiving twice: %v", err)
			}
			var again *int64
			s.db.QueryRowContext(ctx, `SELECT archived_ms FROM runs WHERE id=?`, sd.RunID).Scan(&again)
			if again == nil || *again != *ms {
				t.Error("archiving again rewrote the archive time")
			}
		})
	}
	s, _, _ := newSeededStore(t)
	if err := s.ArchiveRun(ctx, hexID(999)); !errors.Is(err, workflow.ErrConflict) {
		t.Errorf("an unknown run: %v", err)
	}
}

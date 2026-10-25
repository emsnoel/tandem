package state

import (
	"testing"
	"testing/fstest"
	"time"
)

func TestReviewRejectionPersistsAndNotifiesOncePerPayload(t *testing.T) {
	s, sd, job, eff, payload := reviewJobEnv(t)
	r := ReviewRejection{RunID: sd.RunID, AttemptID: sd.AttemptID, JobID: job, EffectID: eff, PayloadHash: payload, Reason: "schema_version is required"}
	if err := s.RecordReviewRejection(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkReviewRejectionNotified(ctx, eff, payload); err != nil {
		t.Fatal(err)
	}
	r.Reason = "duplicate must keep the original diagnosis"
	if err := s.RecordReviewRejection(ctx, r); err != nil {
		t.Fatal(err)
	}
	got, err := s.RunReviewRejections(ctx, sd.RunID)
	if err != nil || len(got) != 1 || !got[0].Notified || got[0].Reason != "schema_version is required" {
		t.Fatalf("repeat: %+v %v", got, err)
	}
	r.PayloadHash = hash64("unimported invalid pane payload")
	r.Reason = "status is not a reviewer outcome"
	if err := s.RecordReviewRejection(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkReviewRejectionNotified(ctx, eff, payload); err != nil {
		t.Fatal(err)
	}
	got, err = s.RunReviewRejections(ctx, sd.RunID)
	if err != nil || len(got) != 1 || got[0].Notified || got[0].Reason != r.Reason || got[0].PayloadHash != r.PayloadHash {
		t.Fatalf("new payload: %+v %v", got, err)
	}
	if other, err := s.RunReviewRejections(ctx, hexID(9999)); err != nil || len(other) != 0 {
		t.Fatalf("another run: %+v %v", other, err)
	}
	if err := s.ClearReviewRejection(ctx, hexID(9999)); err != nil {
		t.Fatal(err)
	}
	if err := s.ClearReviewRejection(ctx, job); err != nil {
		t.Fatal(err)
	}
	if got, err := s.RunReviewRejections(ctx, sd.RunID); err != nil || len(got) != 0 {
		t.Fatalf("cleared: %+v %v", got, err)
	}
}

func TestReviewRejectionRejectsUnknownReferences(t *testing.T) {
	s, sd, job, eff, payload := reviewJobEnv(t)
	for name, mutate := range map[string]func(*ReviewRejection){
		"run":     func(r *ReviewRejection) { r.RunID = hexID(9999) },
		"attempt": func(r *ReviewRejection) { r.AttemptID = hexID(9999) },
		"job":     func(r *ReviewRejection) { r.JobID = hexID(9999) },
		"effect":  func(r *ReviewRejection) { r.EffectID = hexID(9999) },
	} {
		t.Run(name, func(t *testing.T) {
			r := ReviewRejection{RunID: sd.RunID, AttemptID: sd.AttemptID, JobID: job, EffectID: eff, PayloadHash: payload, Reason: "bad verdict"}
			mutate(&r)
			if err := s.RecordReviewRejection(ctx, r); err == nil {
				t.Fatal("accepted unknown reference")
			}
		})
	}
	if got, err := s.RunReviewRejections(ctx, sd.RunID); err != nil || len(got) != 0 {
		t.Fatalf("invalid record persisted: %+v %v", got, err)
	}
}

func TestReviewRejectionsOrderTracksTheLatestWrite(t *testing.T) {
	s, sd, job, eff, payload := reviewJobEnv(t)
	second := hexID(6000)
	sd.exec(t, `INSERT INTO effects(id,run_id,attempt_id,job_id,source_event_id,kind,ordinal,payload_hash,nonce,state,fence,available_ms,created_ms)
 SELECT ?,run_id,attempt_id,?,source_event_id,kind,1,payload_hash,nonce,state,fence,available_ms,created_ms FROM effects WHERE id=?`, second, job, eff)
	now := s.now()
	s.now = func() time.Time { return now }
	for _, r := range []ReviewRejection{
		{RunID: sd.RunID, AttemptID: sd.AttemptID, JobID: job, EffectID: eff, PayloadHash: payload, Reason: "first"},
		{RunID: sd.RunID, AttemptID: sd.AttemptID, JobID: job, EffectID: second, PayloadHash: payload, Reason: "second"},
	} {
		if err := s.RecordReviewRejection(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.RunReviewRejections(ctx, sd.RunID)
	if err != nil || len(got) != 2 || got[1].Reason != "second" {
		t.Fatalf("latest record: %+v %v", got, err)
	}
	r := got[0]
	r.PayloadHash = sd.artifact(t, "corrected")
	r.Reason = "third"
	if err := s.RecordReviewRejection(ctx, r); err != nil {
		t.Fatal(err)
	}
	got, err = s.RunReviewRejections(ctx, sd.RunID)
	if err != nil || got[1].Reason != "third" {
		t.Fatalf("latest update: %+v %v", got, err)
	}
}

func TestReviewRejectionsReadOnlySchemaFiveHasNoFeedback(t *testing.T) {
	files := fstest.MapFS{}
	migrations, err := loadMigrations(embedded)
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations {
		if migration.version <= 5 {
			files["migrations/"+migration.name] = &fstest.MapFile{Data: []byte(migration.sql)}
		}
	}
	root := newRoot(t)
	old, err := open(ctx, root, files, testHooks{})
	if err != nil {
		t.Fatal(err)
	}
	seed := seedProjectRunTaskAttempt(t, old, 0)
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	reader, err := OpenReadOnly(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	got, err := reader.RunReviewRejections(ctx, seed.RunID)
	if err != nil || len(got) != 0 {
		t.Fatalf("schema-five feedback: %+v %v", got, err)
	}
	if _, err := reader.GetRun(ctx, seed.RunID); err != nil {
		t.Fatalf("existing run unreadable: %v", err)
	}
	if version, err := reader.SchemaVersion(); err != nil || version != 5 {
		t.Fatalf("reader changed schema: %d %v", version, err)
	}
	if tableExists(t, reader, "review_rejections") {
		t.Fatal("read-only feedback lookup migrated the store")
	}
}

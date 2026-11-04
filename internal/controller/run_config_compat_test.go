package controller

import (
	"testing"

	"github.com/emsnoel/tandem/internal/state"
)

// A run stored while --share-context existed carries "share_context":true.
// Reading it back must still work: the field is ignored, not an error.
func TestStoredShareContextStillDecodes(t *testing.T) {
	e := newCEnv(t)
	hash, err := e.c.put(ctx, `{"version":1,"root":"/r","builder_kind":"codex","builder_session":"b","planner_kind":"planner","planner_session":"p","share_context":true}`)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := e.c.config(ctx, state.Run{ConfigHash: hash})
	if err != nil {
		t.Fatalf("a stored run with share_context no longer decodes: %v", err)
	}
	if cfg.BuilderKind != "codex" || cfg.BuilderSession != "b" {
		t.Errorf("the other fields were lost: %+v", cfg)
	}
}

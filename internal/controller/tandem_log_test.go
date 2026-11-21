package controller

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/emsnoel/tandem/internal/workflow"
)

// tandem-ask records each exchange in .tandem-log/<id>.json while a run waits
// on a submitted attempt. That record is tandem's own history, not task work,
// so writing it must not stop the run as source-mutated (issue #91).
func TestTheQuestionLogDoesNotMutateASubmittedAttempt(t *testing.T) {
	t.Parallel()
	for name, excluded := range map[string]bool{
		"ignored through git's exclude file": true,
		"not ignored at all":                 false,
	} {
		t.Run(name, func(t *testing.T) {
			e := newCEnv(t)
			e.start()
			att := e.toReview("task-1", 1)
			if excluded {
				exclude := filepath.Join(e.root, ".git", "info", "exclude")
				os.MkdirAll(filepath.Dir(exclude), 0o755)
				f, err := os.OpenFile(exclude, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
				if err != nil {
					t.Fatal(err)
				}
				f.WriteString("\n/.tandem-log/\n")
				f.Close()
			}
			write(t, e.root, ".tandem-log/0123.json", `{"id":"0123","question":"q","answer":"a"}`+"\n")
			if err := e.review(att, workflow.ReviewStatusPassed); err != nil {
				t.Fatalf("the review of an unchanged tree was refused: %v", err)
			}
			e.tick()
			if st, code := e.runState(); code == pauseSourceMutated {
				t.Fatalf("run %s/%s: the question log counted as a change to the task", st, code)
			}
			if e.tasks()[0].AcceptedAttemptID != att.ID {
				t.Fatal("the attempt was not accepted")
			}
		})
	}
}

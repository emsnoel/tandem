package controller

import (
	"sort"

	"github.com/emsnoel/tandem/internal/workflow"
)

// PauseCodes is every code a run can be paused with, sorted. It is the one list
// bash reads (tandem build pause-codes) to check that each code has a note.
// TestPauseCodesListsEveryConstant fails when a pause constant is not in it.
func PauseCodes() []string {
	codes := []string{
		workflow.PauseStuck, workflow.PauseCheckFailed, workflow.PauseReviewFailed,
		workflow.PauseScheduling, workflow.PauseStopping,
		pauseSourceMutated, pauseWriterUnknown, pauseRepairs, pauseDispatch, pauseReview,
		pauseStopped, pausePlanChanged, pauseContext, pauseMemory, pauseIncomplete,
		pauseAgentExited, pauseNoReviewer, pauseAgentUnsupported, pauseRecovery,
		pauseVerificationIdentity, pauseVerificationStale,
	}
	sort.Strings(codes)
	return codes
}

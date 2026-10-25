package workflow

import (
	"errors"
	"fmt"
	"testing"
)

func TestDomainErrorsAreDistinctAndWrappable(t *testing.T) {
	all := []error{ErrConflict, ErrStaleAttempt, ErrInvalidTransition, ErrCorruptArtifact, ErrIncompleteEvidence,
		ErrDeliveryUnknown, ErrContextTooLarge, ErrMemoryConflict, ErrBusy, ErrUnsupported}
	for i, a := range all {
		if !errors.Is(fmt.Errorf("while storing: %w", a), a) {
			t.Errorf("%v does not survive wrapping", a)
		}
		for j, b := range all {
			if i != j && errors.Is(a, b) {
				t.Errorf("%v matches %v", a, b)
			}
		}
	}
}

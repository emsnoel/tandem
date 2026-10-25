package controller

import (
	"fmt"
	"io"
	"os"

	"github.com/emsnoel/tandem/internal/workflow"
)

// Review delivery modes.
const (
	ReviewDeliveryManual  = "manual"
	ReviewDeliveryMailbox = "mailbox"
)

// validateReviewOptions checks a start request's review opt-in. Mailbox
// delivery needs protocol 3, a collaboration and an acceptance policy;
// protocol 3 needs mailbox, and a policy needs both.
func validateReviewOptions(req StartRequest) error {
	switch req.ReviewDelivery {
	case "", ReviewDeliveryManual:
		if req.ReviewProtocol != 0 && req.ReviewProtocol != 2 {
			return fmt.Errorf("%w: review protocol %d needs --review-delivery mailbox", workflow.ErrInvalidTransition, req.ReviewProtocol)
		}
		if req.AcceptancePolicyPath != "" {
			return fmt.Errorf("%w: an acceptance policy goes with --review-delivery mailbox", workflow.ErrInvalidTransition)
		}
	case ReviewDeliveryMailbox:
		if req.ReviewProtocol != 3 {
			return fmt.Errorf("%w: mailbox review delivery is review protocol 3, not %d", workflow.ErrInvalidTransition, req.ReviewProtocol)
		}
		if req.Collaboration == "" {
			return fmt.Errorf("%w: mailbox review delivery needs a collaboration to account for the reviews", workflow.ErrInvalidTransition)
		}
		if req.AcceptancePolicyPath == "" {
			return fmt.Errorf("%w: mailbox review delivery needs an acceptance policy: both review stages are mandatory", workflow.ErrInvalidTransition)
		}
	default:
		return fmt.Errorf("%w: unknown review delivery %q", workflow.ErrInvalidTransition, req.ReviewDelivery)
	}
	if req.RecoveryPolicyPath != "" && req.Collaboration == "" {
		return fmt.Errorf("%w: bounded recovery draws on a collaboration's repair allowance, so it needs --collaboration", workflow.ErrInvalidTransition)
	}
	return nil
}

// loadAcceptancePolicy reads and validates the policy file of a mailbox run.
func loadAcceptancePolicy(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("%w: acceptance policy: %v", workflow.ErrInvalidTransition, err)
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxPolicyBytes+1))
	if err != nil {
		return nil, err
	}
	if _, err := ParseAcceptancePolicy(raw); err != nil {
		return nil, fmt.Errorf("%w: %v", workflow.ErrInvalidTransition, err)
	}
	return raw, nil
}

package main

// The opt-in to direct review handback at the command line.
//
//	tandem build start ... --collaboration ID --review-delivery mailbox --acceptance-policy FILE
//
// Nothing here changes a run that was started without it: the default is the
// manual delivery of review protocol 2, and a run keeps the protocol it was
// started with whatever the environment says later.

import (
	"errors"
	"fmt"

	"github.com/emsnoel/tandem/internal/controller"
)

// validateReviewDelivery checks the three flags together before anything is
// created. Mailbox delivery accounts for its reviews in a collaboration and
// has two mandatory review stages, so it names both; manual delivery takes
// neither.
func validateReviewDelivery(delivery, collaborationID, policyPath string) error {
	switch delivery {
	case "", controller.ReviewDeliveryManual:
		if policyPath != "" {
			return errors.New("--acceptance-policy goes with --review-delivery mailbox")
		}
		return nil
	case controller.ReviewDeliveryMailbox:
		if collaborationID == "" {
			return errors.New("--review-delivery mailbox needs --collaboration, to account for the reviews")
		}
		if policyPath == "" {
			return errors.New("--review-delivery mailbox needs --acceptance-policy: both review stages are mandatory")
		}
		return nil
	}
	return fmt.Errorf("unknown --review-delivery %q (manual or mailbox)", delivery)
}

// reviewProtocolFor is the review protocol a delivery mode stores.
func reviewProtocolFor(delivery string) int {
	if delivery == controller.ReviewDeliveryMailbox {
		return 3
	}
	return 0
}

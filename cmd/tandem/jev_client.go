package main

import (
	"context"
	"time"

	"github.com/emsnoel/tandem/internal/decision"
	"github.com/emsnoel/tandem/internal/decision/jev"
)

// jevClient is the Jev call `decision assess` makes; an interface so tests can
// see whether a request was made.
type jevClient interface {
	Ask(ctx context.Context, model string, state any,
		questions map[string]decision.Question) (map[string]decision.Answer, error)
}

var newJevClient = func(key string, timeout time.Duration) jevClient {
	return jev.New(key, timeout)
}

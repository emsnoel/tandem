package decision

import (
	"context"

	"github.com/emsnoel/tandem/internal/evidence"
)

// Asker turns a snapshot into answers. It lives here so policy and rendering
// never import the transport, and fakes or other providers drop in.
type Asker interface {
	Ask(ctx context.Context, snap evidence.Snapshot) (Answers, error)
}

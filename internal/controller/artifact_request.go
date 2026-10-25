package controller

// Bounded evidence retrieval and context inspection.
//
// A builder may fetch an artifact, a chunk at a time, only if it belongs to its
// own attempt: what the attempt owns, what its jobs produced, its task's
// brief, constraints and spec, and the memory its packet's manifest named. A
// hash grants nothing by itself, and a refusal reads the same whether or not
// the artifact exists.
//
// A sandboxed builder that cannot read the state root sends the same read
// through the mailbox as an artifact_request. Neither path widens what a
// sandbox can reach.

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"regexp"
	"unicode/utf8"

	"github.com/emsnoel/tandem/internal/artifact"
	"github.com/emsnoel/tandem/internal/state"
	"github.com/emsnoel/tandem/internal/workflow"
)

var hashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ErrArtifactDenied is every refusal to serve an artifact: unauthorized, from
// another project, not referenced by anything, or not there. They are one error
// so a caller learns nothing about artifacts it may not read.
var ErrArtifactDenied = errors.New("artifact is not available to this attempt")

// ArtifactRequest names one window of one artifact.
type ArtifactRequest struct {
	Hash   string `json:"hash"`
	Offset int64  `json:"offset"`
	// Limit 0 means artifact.DefaultLimit; more than artifact.MaxLimit is cut to it.
	Limit int `json:"limit"`
}

// ArtifactResult is one chunk. Exactly one of Text and DataBase64 carries bytes,
// and Encoding says which.
type ArtifactResult struct {
	Hash       string `json:"hash"`
	MediaType  string `json:"media_type"`
	Encoding   string `json:"encoding"`
	Role       string `json:"role,omitempty"`
	Offset     int64  `json:"offset"`
	Limit      int    `json:"limit"`
	NextOffset *int64 `json:"next_offset"`
	TotalBytes int64  `json:"total_bytes"`
	Text       string `json:"text,omitempty"`
	DataBase64 string `json:"data_base64,omitempty"`
}

func denied(attemptID, hash string) error {
	return fmt.Errorf("%w: attempt %s may not read %s", ErrArtifactDenied, attemptID, hash)
}

// attemptArtifacts is every artifact attemptID may read, with the reason.
func attemptArtifacts(ctx context.Context, st *state.Store, art *artifact.Store, runID, attemptID string) (map[string]string, error) {
	owner, err := st.AttemptRun(ctx, attemptID)
	if err != nil || owner != runID {
		return nil, denied(attemptID, "anything")
	}
	allowed, err := st.AttemptArtifacts(ctx, attemptID)
	if err != nil {
		if errors.Is(err, workflow.ErrConflict) || errors.Is(err, workflow.ErrStaleAttempt) {
			return nil, denied(attemptID, "anything")
		}
		return nil, err
	}
	mh, found, err := st.AttemptManifest(ctx, attemptID)
	if err != nil || !found {
		return allowed, err
	}
	m, err := readManifest(ctx, art, mh)
	if err != nil {
		return nil, err
	}
	for _, list := range [][]manifestFact{m.Selected, m.Omitted} {
		for _, f := range list {
			e, ok, err := st.GetMemory(ctx, f.ID)
			if err != nil {
				return nil, err
			}
			if !ok || e.RunID != runID {
				continue
			}
			short := f.ID[:12]
			if _, seen := allowed[e.ValueHash]; !seen {
				allowed[e.ValueHash] = "memory value " + short
			}
			if _, seen := allowed[e.SourceHash]; !seen {
				allowed[e.SourceHash] = "memory source " + short
			}
		}
	}
	return allowed, nil
}

// ReadArtifact serves one chunk of an artifact to an attempt, after checking the
// attempt may have it. Any refusal returns no bytes.
func ReadArtifact(ctx context.Context, st *state.Store, art *artifact.Store, runID, attemptID string, req ArtifactRequest) (ArtifactResult, error) {
	switch {
	case req.Offset < 0:
		return ArtifactResult{}, fmt.Errorf("%w: negative offset %d", artifact.ErrInvalidRange, req.Offset)
	case req.Limit < 0:
		return ArtifactResult{}, fmt.Errorf("%w: negative limit %d", artifact.ErrInvalidRange, req.Limit)
	case !hashPattern.MatchString(req.Hash):
		return ArtifactResult{}, fmt.Errorf("%w: %q", artifact.ErrInvalidHash, req.Hash)
	}
	allowed, err := attemptArtifacts(ctx, st, art, runID, attemptID)
	if err != nil {
		return ArtifactResult{}, err
	}
	role, ok := allowed[req.Hash]
	if !ok {
		return ArtifactResult{}, denied(attemptID, req.Hash)
	}
	limit := req.Limit
	if limit == 0 || limit > artifact.MaxLimit {
		limit = artifact.MaxLimit
	}
	chunk, err := art.Read(ctx, req.Hash, req.Offset, limit)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ArtifactResult{}, denied(attemptID, req.Hash)
		}
		return ArtifactResult{}, err
	}
	res := ArtifactResult{Hash: chunk.Hash, Role: role, Offset: chunk.Offset, Limit: limit, NextOffset: chunk.NextOffset, TotalBytes: chunk.TotalBytes}
	data := chunk.Data
	// A window that ends inside a character stops before it, and the next one
	// starts there. The end of a file is not trimmed: truncated text is bytes.
	if chunk.NextOffset != nil {
		for cut := 0; cut < 4 && cut < len(data); cut++ {
			if utf8.Valid(data[:len(data)-cut]) {
				if cut > 0 && !bytes.ContainsRune(data[:len(data)-cut], 0) {
					data = data[:len(data)-cut]
					next := chunk.Offset + int64(len(data))
					res.NextOffset = &next
				}
				break
			}
		}
	}
	if utf8.Valid(data) && !bytes.ContainsRune(data, 0) {
		res.Encoding, res.MediaType, res.Text = "utf8", "text/plain; charset=utf-8", string(data)
	} else {
		res.Encoding, res.MediaType, res.DataBase64 = "base64", "application/octet-stream", base64.StdEncoding.EncodeToString(data)
	}
	return res, nil
}

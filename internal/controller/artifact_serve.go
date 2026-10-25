package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"

	"github.com/emsnoel/tandem/internal/artifact"
	"github.com/emsnoel/tandem/internal/workflow"
)

// ArtifactResponse is what the controller writes into the relay for one
// artifact_request: a chunk, or a refusal with a stable code and no bytes.
type ArtifactResponse struct {
	Status    string          `json:"status"`
	RequestID string          `json:"request_id"`
	Code      string          `json:"code,omitempty"`
	Message   string          `json:"message,omitempty"`
	Result    *ArtifactResult `json:"result,omitempty"`
}

// serveArtifactRequest answers an artifact_request. It changes no state: a read
// is not an event, so there is nothing to apply and nothing to acknowledge.
func serveArtifactRequest(ctx context.Context, deps MailboxDeps, env Envelope, attempt workflow.Attempt, payloadHash string) (workflow.Receipt, error) {
	resp := ArtifactResponse{RequestID: env.EventID}
	refuse := func(code, msg string) { resp.Status, resp.Code, resp.Message = "refused", code, msg }
	raw, err := deps.Artifacts.ReadAll(ctx, payloadHash, 4096)
	if err != nil {
		return workflow.Receipt{}, err
	}
	var req ArtifactRequest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		refuse("malformed_request", "the request is not a {hash, offset, limit} object")
	} else if _, err := dec.Token(); err != io.EOF {
		refuse("malformed_request", "data after the request")
	} else {
		res, err := ReadArtifact(ctx, deps.State, deps.Artifacts, env.RunID, env.AttemptID, req)
		switch {
		case err == nil:
			resp.Status, resp.Result = "ok", &res
		case errors.Is(err, ErrArtifactDenied):
			refuse("artifact_not_available", "this attempt may not read that artifact")
		case errors.Is(err, artifact.ErrInvalidRange), errors.Is(err, artifact.ErrInvalidHash):
			refuse("invalid_request", err.Error())
		case errors.Is(err, workflow.ErrCorruptArtifact):
			refuse("artifact_corrupt", "the artifact does not match its hash")
		default:
			return workflow.Receipt{}, err // transient: the request stays queued
		}
	}
	body, err := json.Marshal(resp)
	if err != nil {
		return workflow.Receipt{}, err
	}
	if err := atomicWrite(filepath.Join(deps.Dir, "responses"), env.EventID+".json", append(body, '\n')); err != nil {
		return workflow.Receipt{}, err
	}
	return workflow.Receipt{EventID: env.EventID, Version: attempt.Version}, nil
}

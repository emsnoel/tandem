// Package jev is the HTTP transport for TypeSafe's System One endpoint. It
// turns a snapshot into a request and a response into answers or an error, and
// decides nothing. internal/decision gives answers meaning and never imports
// this package.
package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/emsnoel/tandem/internal/decision"
)

// Host is the one host the transport talks to. Exported so the builder's
// network allowlist is derived from it rather than restating it: an allowlist
// that drifted from the endpoint would block every call it exists to permit.
const Host = "api.typesafe.ai"

const endpoint = "https://" + Host + "/v1/systemone"

// ErrUnauthorized means the provider refused the key, usually because the Jev
// subscription is inactive. It is not transient.
var ErrUnauthorized = errors.New("TypeSafe rejected TYPESAFE_API_KEY; the Jev subscription may be inactive or lapsed")

type Client struct {
	key  string
	http *http.Client
	// url is the endpoint. A field only so this package's tests can point it
	// at a local server; nothing outside the package can set it.
	url string
}

// New returns a client bound to a timeout. Handoff is interactive, so an
// advisory must never hang it; a call takes about 0.7s and callers pass about
// 2s.
func New(apiKey string, timeout time.Duration) *Client {
	return &Client{key: apiKey, http: &http.Client{Timeout: timeout}, url: endpoint}
}

type request struct {
	Model string `json:"model"`
	// State is `any` rather than evidence.Snapshot: this package transports
	// whatever a caller wants judged, and a second question set judges
	// something that is not a plan. The shape is the caller's contract with
	// the provider, not this client's business.
	State     any                          `json:"state"`
	Questions map[string]decision.Question `json:"questions"`
}

type response struct {
	Model   string                     `json:"model"`
	Answers map[string]decision.Answer `json:"answers"`
}

// Ask sends one batched request and returns the raw answers, after checking
// they came from the model that was asked.
func (c *Client) Ask(ctx context.Context, model string, state any,
	questions map[string]decision.Question) (map[string]decision.Answer, error) {

	body, err := json.Marshal(request{Model: model, State: state, Questions: questions})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("%w (HTTP %d)", ErrUnauthorized, resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		// The body is not included: it echoes the request, which contains the
		// state, and this error may reach a log.
		return nil, fmt.Errorf("jev returned %d", resp.StatusCode)
	}
	return DecodeAnswers(buf.Bytes(), model)
}

// DecodeAnswers turns a response body into raw answers, refusing one from a
// model other than the one asked, since mislabelled calibration data cannot be
// detected later.
func DecodeAnswers(body []byte, wantModel string) (map[string]decision.Answer, error) {
	var r response
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}
	if r.Model != wantModel {
		return nil, fmt.Errorf("response came from %q, not the pinned %q", r.Model, wantModel)
	}
	return r.Answers, nil
}

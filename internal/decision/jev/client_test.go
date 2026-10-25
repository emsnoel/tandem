package jev

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/emsnoel/tandem/internal/decision"
)

// The transport's own tests: decoding a body, and refusing one from a model
// nobody asked. Staffing schema rejections are tested in
// internal/decision/parse_test.go.

// Decoding is tested against a recorded real response, run all the way through
// ParseStaffing, so a provider shape change fails here.
func TestDecodeRecordedResponseParsesIntoStaffingAnswers(t *testing.T) {
	body, err := os.ReadFile("testdata/response.json")
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	raw, err := DecodeAnswers(body, decision.Model)
	if err != nil {
		t.Fatalf("DecodeAnswers: %v", err)
	}
	got, err := decision.ParseStaffing(raw)
	if err != nil {
		t.Fatalf("ParseStaffing: %v", err)
	}
	if got.Shape != decision.ShapeMechanical {
		t.Errorf("shape: want mechanical, got %q", got.Shape)
	}
	if got.ShapeConfidence != 1.0 {
		t.Errorf("shape confidence: want 1.0, got %v", got.ShapeConfidence)
	}
	if got.RoutineProportion != 2.98 {
		t.Errorf("routine proportion: want the weighted float 2.98, got %v", got.RoutineProportion)
	}
	if got.Security != 0.98 {
		t.Errorf("security: want 0.98, got %v", got.Security)
	}
}

// A response from an unpinned model would be attributed to the wrong version,
// which no downstream check can detect.
func TestDecodeRejectsAModelMismatch(t *testing.T) {
	_, err := DecodeAnswers([]byte(`{"model":"jev-2.0.0","answers":{}}`), decision.Model)
	if err == nil {
		t.Fatal("want an error when the responding model is not the pinned one, got nil")
	}
	if !strings.Contains(err.Error(), "jev-2.0.0") {
		t.Fatalf("want the error to name the wrong model, got %v", err)
	}
}

// The model is a parameter now, not a constant read from the contract package,
// so a caller asking a different model must be held to THAT model rather than
// to the staffing one.
func TestDecodeChecksAgainstTheModelThatWasAsked(t *testing.T) {
	body := []byte(`{"model":"some-other-model","answers":{}}`)
	if _, err := DecodeAnswers(body, "some-other-model"); err != nil {
		t.Fatalf("a matching model must be accepted whatever it is: %v", err)
	}
	if _, err := DecodeAnswers(body, decision.Model); err == nil {
		t.Fatal("want an error when the body's model is not the one asked for, got nil")
	}
}

func TestDecodeRejectsAMalformedBody(t *testing.T) {
	_, err := DecodeAnswers([]byte(`{not json`), decision.Model)
	if err == nil {
		t.Fatal("want an error for a body that is not JSON, got nil")
	}
	if !strings.Contains(err.Error(), "decoding response") {
		t.Fatalf("want a decoding error, got %v", err)
	}
}

// The allowlist is built from Host, so Host must be the host every request
// actually goes to.
func TestHostIsTheEndpointsHost(t *testing.T) {
	u, err := url.Parse(endpoint)
	if err != nil {
		t.Fatalf("endpoint does not parse: %v", err)
	}
	if u.Scheme != "https" || u.Host != Host {
		t.Fatalf("endpoint %q: want https://%s/..., got scheme %q host %q", endpoint, Host, u.Scheme, u.Host)
	}
}

// A rejected key usually means an inactive Jev subscription, so it must be
// distinguishable from other failures without parsing a message.
func TestAskReportsARejectedKeyAsUnauthorized(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusInternalServerError} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(code)
		}))
		c := New("ts-test", time.Second)
		c.url = srv.URL
		_, err := c.Ask(context.Background(), decision.Model, map[string]string{}, decision.Questions())
		srv.Close()

		rejected := code != http.StatusInternalServerError
		if errors.Is(err, ErrUnauthorized) != rejected {
			t.Errorf("HTTP %d: errors.Is(ErrUnauthorized)=%t, want %t (err: %v)",
				code, errors.Is(err, ErrUnauthorized), rejected, err)
		}
		if rejected && !strings.Contains(err.Error(), "subscription") {
			t.Errorf("HTTP %d: the message must name the likely cause, got %q", code, err)
		}
	}
}

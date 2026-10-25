package herdr

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// subscriptionSchema reads Herdr's Subscription schema, captured from `herdr
// api schema --json`, as the required fields of each type.
func subscriptionSchema(t *testing.T) map[string][]string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "schema", "subscription-0.9.3.json"))
	if err != nil {
		t.Fatal(err)
	}
	var sch struct {
		OneOf []struct {
			Properties struct {
				Type struct {
					Const string `json:"const"`
				} `json:"type"`
			} `json:"properties"`
			Required []string `json:"required"`
		} `json:"oneOf"`
	}
	if err := json.Unmarshal(b, &sch); err != nil {
		t.Fatal(err)
	}
	req := map[string][]string{}
	for _, o := range sch.OneOf {
		req[o.Properties.Type.Const] = o.Required
	}
	return req
}

// schemaProblems lists what Herdr would reject in subs.
func schemaProblems(required map[string][]string, subs []map[string]any) []string {
	var bad []string
	for _, sub := range subs {
		typ, _ := sub["type"].(string)
		fields, known := required[typ]
		if !known {
			bad = append(bad, "unknown type "+typ)
			continue
		}
		for _, f := range fields {
			if v, ok := sub[f].(string); !ok || v == "" {
				bad = append(bad, typ+" lacks "+f)
			}
		}
	}
	return bad
}

func TestSchemaCheckCatchesAStatusSubscriptionWithoutPane(t *testing.T) {
	bad := schemaProblems(subscriptionSchema(t), []map[string]any{{"type": "pane.agent_status_changed"}})
	if len(bad) != 1 || !strings.Contains(bad[0], "pane_id") {
		t.Fatalf("problems = %v, want the missing pane_id", bad)
	}
}

func TestWatchRequestMeetsHerdrsSubscriptionSchema(t *testing.T) {
	s := newFakeServer(t)
	watchFake(t, s, map[string]*Agent{"builder": agentAt("builder", "w1:p1", "idle"), "reviewer": agentAt("reviewer", "w1:p2", "idle")}, "builder", "reviewer")
	subs := <-s.subs
	if bad := schemaProblems(subscriptionSchema(t), subs); len(bad) > 0 {
		t.Fatalf("Herdr 0.9.3 would reject the subscription: %v", bad)
	}
	status := map[string]bool{}
	for _, sub := range subs {
		if sub["type"] == "pane.agent_status_changed" {
			status[sub["pane_id"].(string)] = true
		}
	}
	if !status["w1:p1"] || !status["w1:p2"] || len(status) != 2 {
		t.Errorf("status subscriptions cover %v, want exactly the two watched panes", status)
	}
}

func TestFakeServerRejectsAStatusSubscriptionWithoutPane(t *testing.T) {
	s := newFakeServer(t)
	c, err := s.dial()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Write([]byte(`{"id":"x","method":"events.subscribe","params":{"subscriptions":[{"type":"pane.agent_status_changed"}]}}` + "\n"))
	buf := make([]byte, 512)
	n, _ := c.Read(buf)
	if e := decodeError(buf[:n]); e == nil || e.Code != "invalid_request" {
		t.Fatalf("answer %q, want invalid_request", buf[:n])
	}
}

// moving is a get whose pane id is panes[i] on its i-th call, then the last.
func moving(panes ...string) func(string) (*Agent, error) {
	i := 0
	return func(n string) (*Agent, error) {
		p := panes[len(panes)-1]
		if i < len(panes) {
			p = panes[i]
		}
		i++
		return agentAt(n, p, "idle"), nil
	}
}

func TestWatchOpensAgainWhenThePaneMovedBeforeTheSnapshot(t *testing.T) {
	s := newFakeServer(t)
	w, err := watchOn(s.dial, moving("w1:p1", "w2:p4"), 8, "builder")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if s.openCount() != 2 || w.Snapshot("builder").PaneID != "w2:p4" {
		t.Fatalf("opens=%d snapshot=%+v, want a second open on the new pane", s.openCount(), w.Snapshot("builder"))
	}
	<-s.subs
	if second := <-s.subs; second[0]["pane_id"] != "w2:p4" {
		t.Errorf("second subscription %v, want the new pane", second[0])
	}
}

func TestWatchIsUnreachableWhenThePaneMovesTwice(t *testing.T) {
	s := newFakeServer(t)
	_, err := watchOn(s.dial, moving("w1:p1", "w2:p4", "w2:p4", "w3:p7"), 8, "builder")
	var he *Error
	if !asError(err, &he) || !he.Unreachable || !strings.Contains(he.Message, "builder") {
		t.Fatalf("err = %v, want unreachable naming builder", err)
	}
	if s.openCount() != 2 {
		t.Errorf("opens = %d, want 2", s.openCount())
	}
}

func TestWatchReturnsAgentNotFoundWhenTheAgentLeftBeforeTheSubscription(t *testing.T) {
	s := newFakeServer(t)
	s.setMissing("w1:p1", true)
	calls := 0
	get := func(n string) (*Agent, error) {
		calls++
		if calls > 1 {
			return nil, &Error{Code: "agent_not_found", Message: "agent target " + n + " not found"}
		}
		return agentAt(n, "w1:p1", "idle"), nil
	}
	_, err := watchOn(s.dial, get, 8, "builder")
	var he *Error
	if !asError(err, &he) || he.Code != "agent_not_found" || !IsGone(err) {
		t.Fatalf("err = %v, want agent_not_found", err)
	}
}

func TestWatchPaneNotFoundTwiceIsGone(t *testing.T) {
	s := newFakeServer(t)
	s.setMissing("w1:p2", true)
	snap := map[string]*Agent{"builder": agentAt("builder", "w1:p1", "idle"), "reviewer": agentAt("reviewer", "w1:p2", "idle")}
	_, err := watchOn(s.dial, func(n string) (*Agent, error) { return snap[n], nil }, 8, "builder", "reviewer")
	var he *Error
	if !asError(err, &he) || he.Code != "pane_not_found" || !IsGone(err) || he.Unreachable {
		t.Fatalf("err = %v, want pane_not_found counted as gone", err)
	}
	if !strings.HasPrefix(he.Message, "reviewer ") {
		t.Errorf("message %q does not name the agent that left", he.Message)
	}
	if s.openCount() != 2 {
		t.Errorf("opens = %d, want one retry", s.openCount())
	}
}

func goneAgent(t *testing.T, missing string, panes map[string]string, names ...string) *Error {
	t.Helper()
	s := newFakeServer(t)
	s.setMissing(missing, true)
	get := func(n string) (*Agent, error) { return agentAt(n, panes[n], "idle"), nil }
	_, err := watchOn(s.dial, get, 8, names...)
	var he *Error
	if !asError(err, &he) || he.Code != "pane_not_found" || !IsGone(err) {
		t.Fatalf("err = %v, want pane_not_found counted as gone", err)
	}
	return he
}

// "pane w1:p12 not found" contains "w1:p1": the agent on p1 must not be named.
func TestPaneNotFoundNamesTheAgentOnThatExactPane(t *testing.T) {
	he := goneAgent(t, "w1:p12", map[string]string{"builder": "w1:p1", "reviewer": "w1:p12"}, "builder", "reviewer")
	if he.Agent != "reviewer" || !strings.HasPrefix(he.Message, "reviewer ") {
		t.Errorf("Agent = %q, message %q, want reviewer", he.Agent, he.Message)
	}
}

func TestPaneNotFoundNamesTheLongerAgentName(t *testing.T) {
	he := goneAgent(t, "w1:p2", map[string]string{"builder": "w1:p1", "builder-2": "w1:p2"}, "builder", "builder-2")
	if he.Agent != "builder-2" {
		t.Errorf("Agent = %q, want builder-2", he.Agent)
	}
}

func TestAgentNotFoundCarriesTheNameItWasAskedFor(t *testing.T) {
	s := newFakeServer(t)
	get := func(n string) (*Agent, error) {
		if n == "builder-2" {
			return nil, &Error{Code: "agent_not_found", Message: "agent target builder-2 not found"}
		}
		return agentAt(n, "w1:p1", "idle"), nil
	}
	_, err := watchOn(s.dial, get, 8, "builder", "builder-2")
	var he *Error
	if !asError(err, &he) || !IsGone(err) || he.Agent != "builder-2" {
		t.Fatalf("err = %v, want agent_not_found naming builder-2", err)
	}
}

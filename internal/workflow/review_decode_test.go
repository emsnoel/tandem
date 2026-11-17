package workflow

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestDecodeReviewBindsVerdictAndPreservesFullIdentity(t *testing.T) {
	full, job := reviewFixture()
	verdict := Verdict{SchemaVersion: 1, Status: ReviewStatusPassed, Findings: []Finding{}}
	if got := BindReview(verdict, job); !reflect.DeepEqual(got, full) {
		t.Fatalf("bound result = %+v", got)
	}
	for _, value := range []any{verdict, full} {
		raw, _ := json.Marshal(value)
		got, err := DecodeReview(raw, job)
		if err != nil || !reflect.DeepEqual(got, full) {
			t.Fatalf("decode %s: %+v, %v", raw, got, err)
		}
		if err := ValidateVerdictRaw(raw); err != nil {
			t.Fatalf("local validation %s: %v", raw, err)
		}
	}
	full.JobID = strings.Repeat("9", 32)
	raw, _ := json.Marshal(full)
	if err := ValidateVerdictRaw(raw); err != nil {
		t.Fatalf("local shape validation needs no matching job: %v", err)
	}
	if _, err := DecodeReview(raw, job); err == nil {
		t.Fatal("silently rebound another job")
	}
}

func TestDecodeReviewRejectsMalformedOrIncompletePayloads(t *testing.T) {
	full, job := reviewFixture()
	rawFull, _ := json.Marshal(full)
	for name, raw := range map[string]string{
		"missing schema":        `{"status":"passed","findings":[]}`,
		"unknown field":         `{"schema_version":1,"status":"passed","findings":[],"surprise":true}`,
		"partial identity":      `{"schema_version":1,"status":"passed","findings":[],"job_id":"` + job.ID + `"}`,
		"null identity":         `{"schema_version":1,"status":"passed","findings":[],"job_id":null}`,
		"uppercase identity":    `{"schema_version":1,"status":"passed","findings":[],"JOB_ID":"` + job.ID + `"}`,
		"incident ID":           strings.Replace(string(rawFull), job.ID, job.ID+"1", 1),
		"bad hash":              strings.Replace(string(rawFull), job.TreeHash, "abc", 1),
		"unknown status":        `{"schema_version":1,"status":"great","findings":[]}`,
		"empty changes":         `{"schema_version":1,"status":"changes_requested","findings":[]}`,
		"invalid finding":       `{"schema_version":1,"status":"failed","findings":[{"severity":"high","disposition":"open","path":"../secret","message":"x"}]}`,
		"unresolved blocker":    `{"schema_version":1,"status":"passed","findings":[{"severity":"high","disposition":"open","path":"a.go","message":"x"}]}`,
		"unknown finding field": `{"schema_version":1,"status":"failed","findings":[{"severity":"low","disposition":"open","path":"a.go","message":"x","extra":0}]}`,
		"null":                  `null`, "array": `[]`, "empty": ``,
		"multiple objects": `{"schema_version":1,"status":"passed","findings":[]} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeReview([]byte(raw), job); err == nil {
				t.Fatal("accepted invalid payload")
			}
			if err := ValidateVerdictRaw([]byte(raw)); err == nil {
				t.Fatal("local validation accepted invalid payload")
			}
		})
	}
}

// A result that is not JSON says where it stops being JSON: the decoder's
// error, the byte offset and the text just before it (#60).
func TestDecodeReviewNamesTheByteWhereJSONBreaks(t *testing.T) {
	_, job := reviewFixture()
	raw := `{"schema_version":1,"status":"failed","findings":[{"severity":"high","path":"a.go","disposition":"open","message":"it requires: "Lint must pass.""}]}`
	offset := strings.Index(raw, `"Lint`) + 2
	for name, err := range map[string]error{"DecodeReview": func() error { _, err := DecodeReview([]byte(raw), job); return err }(), "ValidateVerdictRaw": ValidateVerdictRaw([]byte(raw))} {
		if err == nil {
			t.Fatalf("%s accepted unescaped quotes", name)
		}
		for _, want := range []string{"not valid JSON", "invalid character 'L'", fmt.Sprintf("at byte %d", offset), `requires: \"L`} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: %q does not say %q", name, err, want)
			}
		}
	}
}

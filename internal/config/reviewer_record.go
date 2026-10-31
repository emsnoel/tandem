package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// ReviewerFile is the record bin/tandem-pair writes in the relay, beside
// builder.json, for the reviewer pane it started or adopted.
const ReviewerFile = "reviewer.json"

// ReviewerRecord is what reviewer.json holds. ModelMeasured is false when the
// pane was launched with no model, or was already running with settings
// tandem-pair never saw.
type ReviewerRecord struct {
	Kind          string `json:"kind"`
	Name          string `json:"name"`
	Model         string `json:"model"`
	Autonomy      string `json:"autonomy"`
	Effort        string `json:"effort,omitempty"`
	ModelMeasured bool   `json:"model_measured"`
}

var (
	// ErrNoReviewer: the relay has no reviewer.json.
	ErrNoReviewer = errors.New("no reviewer is recorded")
	// ErrReviewerIncomplete: reviewer.json exists but cannot be read, or does
	// not name both a kind and a pane.
	ErrReviewerIncomplete = errors.New("the reviewer record cannot be read or names no reviewer kind and name")
)

// LoadReviewer reads relay/reviewer.json. A record is complete when it names
// both a kind and a pane; anything less is ErrReviewerIncomplete, so no
// caller guesses at a half-written record.
func LoadReviewer(relay string) (ReviewerRecord, error) {
	path := filepath.Join(relay, ReviewerFile)
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return ReviewerRecord{}, fmt.Errorf("%w in %s", ErrNoReviewer, path)
	}
	var rec ReviewerRecord
	if err != nil || json.Unmarshal(raw, &rec) != nil || rec.Kind == "" || rec.Name == "" {
		return ReviewerRecord{}, fmt.Errorf("%w: %s", ErrReviewerIncomplete, path)
	}
	return rec, nil
}

// WriteReviewer writes relay/reviewer.json, creating the relay.
func WriteReviewer(relay string, rec ReviewerRecord) error {
	return writeRecord(relay, ReviewerFile, rec)
}

// NextReviewer is the record for a reviewer pane bin/tandem-pair started or
// found running. A started pane's model is known when it was launched with
// one. A running pane keeps prev's model only when prev names the same kind
// and name and measured it; otherwise its model is unknown.
func NextReviewer(prev *ReviewerRecord, kind, name, model, autonomy string, started bool) ReviewerRecord {
	rec := ReviewerRecord{Kind: kind, Name: name, Autonomy: autonomy}
	switch {
	case started:
		rec.Model, rec.ModelMeasured = model, model != ""
	case prev != nil && prev.Kind == kind && prev.Name == name && prev.ModelMeasured && prev.Model != "":
		rec.Model, rec.ModelMeasured = prev.Model, true
	}
	return rec
}

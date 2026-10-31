package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// BuilderFile is the record bin/tandem-pair writes in the relay for the
// builder it started or adopted. tandem-verdict and the review gate read it,
// so they never resolve the builder again and disagree.
const BuilderFile = "builder.json"

// BuilderRecord is what builder.json holds. For an adopted builder it records
// what tandem asked for, which may differ from how the agent was started.
type BuilderRecord struct {
	Kind     string `json:"kind"`
	Autonomy string `json:"autonomy"`
	Adopted  bool   `json:"adopted"`
}

// WriteBuilder writes relay/builder.json, creating the relay.
func WriteBuilder(relay string, rec BuilderRecord) error {
	return writeRecord(relay, BuilderFile, rec)
}

// writeRecord writes v as one JSON line through a .tmp file and a rename, so a
// reader never sees half a record.
func writeRecord(relay, name string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(relay, 0o755); err != nil {
		return err
	}
	path := filepath.Join(relay, name)
	if err := os.WriteFile(path+".tmp", append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

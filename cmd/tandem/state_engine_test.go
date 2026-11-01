package main

import (
	"testing"

	"github.com/emsnoel/tandem/internal/exitcode"
)

func TestStateEngineFieldsDefault(t *testing.T) {
	t.Setenv("TANDEM_BUILD_ENGINE", "")
	rc, out, errOut := runCapture(t, "state", "engine", "--root", t.TempDir(), "--fields")
	if rc != exitcode.OK || out != "legacy\x00the default\x00" {
		t.Errorf("rc %d out %q stderr %q", rc, out, errOut)
	}
}

func TestStateEngineFieldsEnv(t *testing.T) {
	t.Setenv("TANDEM_BUILD_ENGINE", "durable")
	rc, out, errOut := runCapture(t, "state", "engine", "--root", t.TempDir(), "--fields")
	if rc != exitcode.OK || out != "durable\x00TANDEM_BUILD_ENGINE\x00" {
		t.Errorf("rc %d out %q stderr %q", rc, out, errOut)
	}
}

func TestStateEngineJSONUnchanged(t *testing.T) {
	t.Setenv("TANDEM_BUILD_ENGINE", "")
	root := t.TempDir()
	rc, out, _ := runCapture(t, "state", "engine", "--root", root)
	v := oneJSON(t, out)
	if rc != exitcode.OK || v["engine"] != "legacy" || v["because"] != "the default" || v["relay"] == "" {
		t.Errorf("rc %d %v", rc, v)
	}
}

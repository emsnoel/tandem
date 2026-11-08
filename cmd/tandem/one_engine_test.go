package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emsnoel/tandem/internal/exitcode"
	"github.com/emsnoel/tandem/internal/paths"
)

func TestStartIsDurableWithNoSetting(t *testing.T) {
	t.Setenv("TANDEM_BUILD_ENGINE", "")
	e := newCLI(t)
	rc, got, errOut := e.start()
	if rc != exitcode.OK {
		t.Fatalf("start with no engine setting: rc %d %q", rc, errOut)
	}
	if got["engine"] != "durable" {
		t.Errorf("engine %v, want durable", got["engine"])
	}
	if _, err := os.Stat(filepath.Join(paths.Relay(e.root), "active-run.json")); err != nil {
		t.Errorf("no active-run.json after a start: %v", err)
	}
}

func TestEngineSettingRules(t *testing.T) {
	for _, tc := range []struct {
		name, env, manifest string
		refused             string // the setting a refusal must name; empty starts
	}{
		{name: "unset"},
		{name: "env durable", env: "durable"},
		{name: "file durable", manifest: `{"build_engine":"durable"}`},
		{name: "env legacy", env: "legacy", refused: "TANDEM_BUILD_ENGINE"},
		{name: "env typo", env: "durabel", refused: "TANDEM_BUILD_ENGINE"},
		{name: "file legacy", manifest: `{"build_engine":"legacy"}`, refused: "build_engine"},
		{name: "file typo", manifest: `{"build_engine":"durabel"}`, refused: "build_engine"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TANDEM_BUILD_ENGINE", tc.env)
			e := newCLI(t)
			if tc.manifest != "" {
				if err := os.WriteFile(filepath.Join(e.root, ".tandem.json"), []byte(tc.manifest), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			rc, _, errOut := e.start()
			_, markerErr := os.Stat(filepath.Join(paths.Relay(e.root), "active-run.json"))
			if tc.refused == "" {
				if rc != exitcode.OK || markerErr != nil {
					t.Fatalf("rc %d, marker %v, %q", rc, markerErr, errOut)
				}
				return
			}
			if rc == exitcode.OK || !strings.Contains(errOut, tc.refused) || !strings.Contains(errOut, "legacy engine is gone") {
				t.Fatalf("want a refusal naming %s: rc %d %q", tc.refused, rc, errOut)
			}
			if markerErr == nil {
				t.Error("a refused start left a run marker")
			}
			if _, err := os.Stat(e.home); !os.IsNotExist(err) {
				t.Errorf("a refused start wrote state: %v", err)
			}
		})
	}
}

func TestLegacyVerbsAreUnknown(t *testing.T) {
	e := newCLI(t)
	for _, verb := range []string{"sent", "review-sent", "advance", "archive"} {
		if rc, _, errOut := e.run("build", verb, "--root", e.root); rc != exitcode.Usage || !strings.Contains(errOut, "usage: tandem build") {
			t.Errorf("build %s: rc %d %q", verb, rc, errOut)
		}
	}
}

func TestCommandsWithNoRunSayNoRun(t *testing.T) {
	t.Setenv("TANDEM_BUILD_ENGINE", "")
	e := newCLI(t)
	for _, verb := range []string{"accept", "stop", "status", "tick", "wait", "recover", "review-record", "reconcile", "usage", "budget"} {
		rc, _, errOut := e.sub(verb)
		if rc == exitcode.OK || !strings.Contains(errOut, "no durable run in "+paths.Relay(e.root)) {
			t.Errorf("build %s with no run: rc %d %q", verb, rc, errOut)
		}
	}
}

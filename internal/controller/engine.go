package controller

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Engine is which build engine owns a run.
type Engine string

const (
	EngineLegacy  Engine = "legacy"
	EngineDurable Engine = "durable"
)

// SelectEngine decides which engine a relay's commands go to. The environment
// variable only chooses for a relay with no run. Once a run exists its engine
// is fixed by the relay (a durable marker or a legacy build.json). A relay
// with both, or an unreadable marker, is refused.
func SelectEngine(relay, env string) (Engine, error) {
	engine, _, err := SelectEngineWhy(relay, env, "")
	return engine, err
}

// SelectEngineWhy is SelectEngine with the repository's own setting, used when
// env is empty, and the reason for the answer: "the relay's durable run", "the
// relay's legacy build", "TANDEM_BUILD_ENGINE", ".tandem.json" or "the default".
func SelectEngineWhy(relay, env, repo string) (Engine, string, error) {
	run, err := ReadActiveRun(relay)
	if err != nil {
		return "", "", err
	}
	_, statErr := os.Stat(filepath.Join(relay, "build", "build.json"))
	legacy := statErr == nil
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return "", "", statErr
	}
	switch {
	case run != nil && legacy:
		return "", "", fmt.Errorf("%s holds both a durable run (%s) and a legacy build; refusing to guess which owns the next command", relay, run.RunID)
	case run != nil:
		return EngineDurable, "the relay's durable run", nil
	case legacy:
		return EngineLegacy, "the relay's legacy build", nil
	}
	setting, why := env, "TANDEM_BUILD_ENGINE"
	if setting == "" {
		setting, why = repo, ".tandem.json"
	}
	switch setting {
	case "":
		return EngineLegacy, "the default", nil
	case "legacy":
		return EngineLegacy, why, nil
	case "durable":
		return EngineDurable, why, nil
	}
	return "", "", fmt.Errorf("%s: %q is not legacy or durable", why, setting)
}

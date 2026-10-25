// Package herdr talks to Herdr: the herdr CLI for actions (prompt, read, start,
// send-keys, list, get) and the socket's event stream for waiting on changes
// (events.go). Actions stay on the CLI because sandbox detection in Raw relies
// on how it fails. The event shapes read are pinned in guide/design.md.
package herdr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/emsnoel/tandem/internal/exitcode"
)

// Error is a failure reported by herdr, or a failure to reach it at all.
type Error struct {
	Code        string
	Message     string
	Unreachable bool
	// Agent names the watched agent a gone error (agent_not_found or
	// pane_not_found) is about, when a watch could tell. Callers read it
	// instead of searching Message.
	Agent string
}

func (e *Error) Error() string { return e.Code + "\t" + e.Message }

// ExitCodeFor maps an error onto tandem's exit vocabulary.
func ExitCodeFor(err error) int {
	var he *Error
	if errors.As(err, &he) && he.Unreachable {
		return exitcode.Unreachable
	}
	if errors.As(err, &he) {
		return exitcode.HerdrError
	}
	return exitcode.Internal
}

// Raw runs herdr and returns stdout. A non-zero exit becomes an *Error.
func Raw(args ...string) ([]byte, error) {
	return rawContext(context.Background(), args...)
}

// rawContext is Raw bounded by ctx. A herdr still running at the deadline is
// killed, and the failure is an *Error with Code "timeout": herdr neither
// answered nor proved out of reach.
func rawContext(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "herdr", args...)
	// A killed herdr can leave a child holding its output pipes open; stop
	// waiting for them a second after the kill.
	cmd.WaitDelay = time.Second
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	if err == nil {
		return out.Bytes(), nil
	}
	if ctx.Err() != nil {
		return nil, &Error{Code: "timeout", Message: "herdr did not answer: " + ctx.Err().Error()}
	}
	if errors.Is(err, exec.ErrNotFound) {
		return nil, &Error{Code: "not_found", Message: "herdr is not on PATH", Unreachable: true}
	}
	if e := decodeError(errb.Bytes()); e != nil {
		// A denied socket is the Codex seatbelt sandbox, which callers
		// detect to fall back to the mailbox relay. server_not_running is the
		// same situation from the caller's point of view: herdr is not usable
		// from here, so the relay is the only route.
		if e.Code == "server_not_running" || looksUnreachable(e.Code) || looksUnreachable(e.Message) {
			e.Unreachable = true
		}
		return nil, e
	}

	// stderr was not a herdr JSON envelope, so herdr never answered. Under Codex's
	// sandbox the CLI dies before formatting a response and prints a raw
	//   Error: Os { code: 1, kind: PermissionDenied, message: "Operation not permitted" }
	// Any such non-answer is unreachable, so callers fall back to the mailbox.
	return nil, &Error{
		Code:        "unreachable",
		Message:     strings.TrimSpace(errb.String()),
		Unreachable: true,
	}
}

// looksUnreachable reports whether a message describes herdr being out of
// reach rather than herdr declining a request.
func looksUnreachable(s string) bool {
	for _, needle := range []string{
		"PermissionDenied",
		"Operation not permitted",
		"permission denied",
		"No such file or directory",
		"Connection refused",
		"server_not_running",
	} {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

func decodeError(b []byte) *Error {
	var env errEnvelope
	if json.Unmarshal(b, &env) != nil || env.Error == nil {
		return nil
	}
	return &Error{Code: env.Error.Code, Message: env.Error.Message}
}

func decodeList(b []byte) ([]Agent, error) {
	var env listEnvelope
	if err := json.Unmarshal(b, &env); err != nil {
		return nil, fmt.Errorf("herdr agent list: %w", err)
	}
	if env.Result == nil {
		return nil, errors.New("herdr agent list: response has no result field")
	}
	// agent_status is required; see the note on Agent.Status.
	for i, a := range env.Result.Agents {
		if a.Status == nil {
			return nil, fmt.Errorf("herdr agent list: agent %d (%s) has no agent_status; the schema moved", i, a.PaneID)
		}
	}
	return env.Result.Agents, nil
}

func decodeGet(b []byte) (*Agent, error) {
	var env getEnvelope
	if err := json.Unmarshal(b, &env); err != nil {
		return nil, fmt.Errorf("herdr agent get: %w", err)
	}
	if env.Result == nil || env.Result.Agent == nil {
		return nil, errors.New("herdr agent get: response has no result.agent field")
	}
	return env.Result.Agent, nil
}

// List performs exactly one `herdr agent list`, so pane_id, cwd and
// agent_status describe the same instant.
func List() ([]Agent, error) {
	b, err := Raw("agent", "list")
	if err != nil {
		return nil, err
	}
	return decodeList(b)
}

// ListWithin is List that gives up after d, with an *Error whose Code is
// "timeout" (IsTimeout).
func ListWithin(d time.Duration) ([]Agent, error) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	b, err := rawContext(ctx, "agent", "list")
	if err != nil {
		return nil, err
	}
	return decodeList(b)
}

// IsTimeout reports whether err is a herdr call that did not answer in time.
func IsTimeout(err error) bool {
	var he *Error
	return errors.As(err, &he) && he.Code == "timeout"
}

// SocketUnreachable reports whether err says this shell cannot reach Herdr's
// socket at all: no server, a refused connection, a denied one (the Codex
// seatbelt) or a missing socket. It is narrower than Error.Unreachable, which
// also covers a missing herdr CLI and any answer that cannot be read; those
// are failures, not proof that a sandbox stands in the way.
func SocketUnreachable(err error) bool {
	var he *Error
	if !errors.As(err, &he) || he.Code == "not_found" || he.Code == "timeout" {
		return false
	}
	return he.Code == "server_not_running" || looksUnreachable(he.Code) || looksUnreachable(he.Message)
}

func Get(name string) (*Agent, error) {
	b, err := Raw("agent", "get", name)
	if err != nil {
		return nil, err
	}
	return decodeGet(b)
}

// Find returns the first agent with the given name, as a pointer into agents.
func Find(agents []Agent, name string) *Agent {
	for i := range agents {
		if agents[i].Name != nil && *agents[i].Name == name {
			return &agents[i]
		}
	}
	return nil
}

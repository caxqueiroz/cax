package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os/exec"
	"strings"
	"time"
)

// defaultTimeoutSeconds is applied when an Entry sets TimeoutSeconds <= 0.
// Matches Claude Code's documented default of 5 seconds.
const defaultTimeoutSeconds = 5

// Dispatcher runs plugin-declared hooks for the four lifecycle events. It is
// safe to call methods on a nil *Dispatcher: Dispatch returns a no-op Result
// and Entries returns nil. This keeps wiring in agent.Build trivial when no
// plugins contribute hooks.
type Dispatcher struct {
	entries []Entry
	logger  *slog.Logger
}

// Load constructs a Dispatcher over the given entries. The logger is used for
// best-effort error reporting (spawn failures, timeouts) and must not be nil;
// callers should pass slog.Default() when they have no specific logger.
func Load(entries []Entry, logger *slog.Logger) *Dispatcher {
	if logger == nil {
		logger = slog.Default()
	}
	cp := make([]Entry, len(entries))
	copy(cp, entries)
	return &Dispatcher{entries: cp, logger: logger}
}

// Entries returns the registered hook entries. The returned slice is a copy
// safe for callers to read (e.g. /hooks listing) without mutating dispatcher
// state. nil-safe: returns nil if d is nil.
func (d *Dispatcher) Entries() []Entry {
	if d == nil {
		return nil
	}
	cp := make([]Entry, len(d.entries))
	copy(cp, d.entries)
	return cp
}

// Dispatch runs every entry whose Event + Matcher matches ev and the payload's
// tool/command (extracted internally). It returns Block=true if any matching
// hook exited non-zero, joining their stdout into Feedback. Errors (spawn,
// timeout, non-exit-code failures) are logged and treated as no-ops so a
// broken hook never blocks the agent.
//
// nil-safe: returns a no-op Result if d is nil.
func (d *Dispatcher) Dispatch(ctx context.Context, ev Event, payload any) Result {
	if d == nil || len(d.entries) == 0 {
		return Result{}
	}
	toolName, command := payloadToolCommand(payload)
	var result Result
	for _, e := range d.entries {
		if !matches(e, ev, toolName, command) {
			continue
		}
		r := d.runOne(ctx, e, ev, payload)
		if r.Block {
			result.Block = true
			result.Feedback = joinFeedback(result.Feedback, r.Feedback)
		}
	}
	return result
}

// runOne executes a single matching Entry. The envelope is JSON-encoded onto
// stdin; stdout is trimmed and returned as Feedback when the child exits
// non-zero. Spawn errors, timeouts, and non-exit-code failures are logged and
// treated as no-ops (best-effort: a broken hook must never block the agent).
func (d *Dispatcher) runOne(ctx context.Context, e Entry, ev Event, payload any) Result {
	if len(e.Command) == 0 {
		d.logger.Warn("hook: empty command, skipping", "event", string(ev), "source", e.Source)
		return Result{}
	}

	envelope, err := buildEnvelope(ev, payload)
	if err != nil {
		d.logger.Warn("hook: failed to build envelope", "event", string(ev), "source", e.Source, "err", err)
		return Result{}
	}

	timeout := time.Duration(e.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = time.Duration(defaultTimeoutSeconds) * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, e.Command[0], e.Command[1:]...)
	cmd.Stdin = bytes.NewReader(envelope)
	// WaitDelay forces Wait to return shortly after the context is cancelled
	// and the process is SIGKILL'd, even if I/O pipes inherited by a child of
	// our child (e.g. /bin/sh -c "sleep 10; ...") keep the stdout pipe open.
	// Without it, Run blocks until the orphaned child closes the pipe, which
	// defeats the per-entry timeout.
	cmd.WaitDelay = 500 * time.Millisecond
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()
	if runCtx.Err() == context.DeadlineExceeded {
		d.logger.Warn("hook: timeout, killed", "event", string(ev), "source", e.Source, "timeout", timeout)
		return Result{}
	}
	if err == nil {
		return Result{} // exit 0 = no block
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		// Non-zero exit blocks; stdout is the feedback the model sees.
		feedback := strings.TrimSpace(stdout.String())
		if feedback == "" {
			feedback = strings.TrimSpace(stderr.String())
		}
		return Result{Block: true, Feedback: feedback}
	}

	// Spawn errors and other non-exit-code failures: log and skip.
	d.logger.Warn("hook: exec failed", "event", string(ev), "source", e.Source, "err", err)
	return Result{}
}

// payloadToolCommand extracts tool_name + command from a payload map. Both are
// optional; missing/non-string fields return "". The payload shape mirrors
// Claude Code's hook envelope: {"tool_name": "Bash", "tool_input": {"command": "..."}}.
func payloadToolCommand(payload any) (toolName, command string) {
	m, ok := payload.(map[string]any)
	if !ok {
		return "", ""
	}
	if v, ok := m["tool_name"].(string); ok {
		toolName = v
	}
	if ti, ok := m["tool_input"].(map[string]any); ok {
		if v, ok := ti["command"].(string); ok {
			command = v
		}
	}
	return toolName, command
}

// joinFeedback concatenates non-empty feedback strings with a blank line so the
// model can read multiple blocking hooks' outputs in one Result.Feedback.
func joinFeedback(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	return a + "\n\n" + b
}

// buildEnvelope renders the JSON object written to a hook's stdin. The shape
// mirrors Claude Code's hook envelope so plugins authored for Claude Code work
// in czcli: a base {hook_event_name, ...} merged with any payload-supplied
// fields (e.g. tool_name, tool_input, prompt).
func buildEnvelope(ev Event, payload any) ([]byte, error) {
	env := map[string]any{
		"hook_event_name": string(ev),
	}
	if m, ok := payload.(map[string]any); ok {
		for k, v := range m {
			env[k] = v
		}
	}
	return json.Marshal(env)
}

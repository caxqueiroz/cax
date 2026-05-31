// Package bgproc tracks long-running shell commands launched via the
// bash_bg tool so the agent can poll them with bash_status (and so
// PreGeneration can inject "task <id> completed" notices into the next turn
// without the model having to ask).
package bgproc

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os/exec"
	"sync"
	"time"

	"github.com/deepnoodle-ai/dive/toolkit"
)

// Status is the lifecycle a background process moves through.
type Status string

const (
	StatusRunning   Status = "running"
	StatusCompleted Status = "completed"
	StatusFailed    Status = "failed"
)

// maxBufBytes caps each of stdout/stderr per process to keep memory bounded.
// When exceeded, we trim from the head and emit a "...[truncated]..." marker.
const maxBufBytes = 32 * 1024

// Process holds the live state of a single background command. All fields are
// guarded by Registry.mu — callers should use the snapshot returned from
// Status() rather than this pointer directly.
type Process struct {
	ID         string
	Command    string
	WorkingDir string
	Status     Status
	ExitCode   int
	Stdout     []byte
	Stderr     []byte
	Started    time.Time
	Finished   time.Time
	Err        error // non-nil when StatusFailed and the process didn't start

	stdoutBuf bytes.Buffer
	stderrBuf bytes.Buffer
	cancel    context.CancelFunc
	notified  bool // PreGeneration has already announced this completion
}

// Registry tracks all background processes for one agent.
type Registry struct {
	mu        sync.Mutex
	procs     map[string]*Process
	validator *toolkit.PathValidator // optional; gates working_dir reads
}

// New constructs a Registry. validator may be nil (no path enforcement).
func New(validator *toolkit.PathValidator) *Registry {
	return &Registry{
		procs:     make(map[string]*Process),
		validator: validator,
	}
}

// Start spawns command in a subshell and returns its task id. The process
// runs detached from ctx (so the calling turn ending doesn't kill it); use
// Stop to terminate. workingDir, when non-empty, must pass validator.ValidateRead.
func (r *Registry) Start(command, workingDir string) (string, error) {
	if command == "" {
		return "", fmt.Errorf("bg: command required")
	}
	if workingDir != "" && r.validator != nil {
		if err := r.validator.ValidateRead(workingDir); err != nil {
			return "", err
		}
	}
	id, err := newID()
	if err != nil {
		return "", fmt.Errorf("bg: gen id: %w", err)
	}

	procCtx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(procCtx, "bash", "-c", command)
	if workingDir != "" {
		cmd.Dir = workingDir
	}

	p := &Process{
		ID:         id,
		Command:    command,
		WorkingDir: workingDir,
		Status:     StatusRunning,
		Started:    time.Now(),
		cancel:     cancel,
	}
	cmd.Stdout = &writerAdapter{p: p, which: "stdout"}
	cmd.Stderr = &writerAdapter{p: p, which: "stderr"}

	if err := cmd.Start(); err != nil {
		cancel()
		p.Status = StatusFailed
		p.Err = err
		p.Finished = time.Now()
		r.mu.Lock()
		r.procs[id] = p
		r.mu.Unlock()
		return id, nil // id still returned so the model can read the failure via bash_status
	}

	r.mu.Lock()
	r.procs[id] = p
	r.mu.Unlock()

	slog.Info("bg: started", "id", id, "command", command, "dir", workingDir)

	go func() {
		err := cmd.Wait()
		r.mu.Lock()
		defer r.mu.Unlock()
		p.Finished = time.Now()
		p.Stdout = capTail(p.stdoutBuf.Bytes())
		p.Stderr = capTail(p.stderrBuf.Bytes())
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				p.ExitCode = exitErr.ExitCode()
				p.Status = StatusFailed
			} else {
				p.Err = err
				p.Status = StatusFailed
			}
		} else {
			p.ExitCode = 0
			p.Status = StatusCompleted
		}
		slog.Info("bg: finished",
			"id", id,
			"status", p.Status,
			"exit_code", p.ExitCode,
			"elapsed_ms", p.Finished.Sub(p.Started).Milliseconds(),
		)
	}()

	return id, nil
}

// Status returns a SNAPSHOT of the process state (safe to inspect outside the
// lock). ok==false when id is unknown.
func (r *Registry) Status(id string) (Process, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.procs[id]
	if !ok {
		return Process{}, false
	}
	return Process{
		ID:         p.ID,
		Command:    p.Command,
		WorkingDir: p.WorkingDir,
		Status:     p.Status,
		ExitCode:   p.ExitCode,
		Stdout:     capTail(p.stdoutBuf.Bytes()),
		Stderr:     capTail(p.stderrBuf.Bytes()),
		Started:    p.Started,
		Finished:   p.Finished,
		Err:        p.Err,
	}, true
}

// Stop sends ctx-cancel to the process. No-op if already finished or unknown.
func (r *Registry) Stop(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.procs[id]
	if !ok {
		return fmt.Errorf("bg: unknown task id %q", id)
	}
	if p.Status != StatusRunning {
		return nil
	}
	p.cancel()
	return nil
}

// DrainCompletionNotices returns a short text summary for every process that
// finished since the previous call, then marks them as notified. PreGeneration
// uses this to slip "background task X finished" into the next system prompt.
func (r *Registry) DrainCompletionNotices() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, p := range r.procs {
		if p.Status == StatusRunning || p.notified {
			continue
		}
		p.notified = true
		summary := fmt.Sprintf("background task %s (%s) %s with exit %d in %s",
			p.ID,
			truncate(p.Command, 60),
			p.Status,
			p.ExitCode,
			p.Finished.Sub(p.Started).Round(time.Millisecond),
		)
		if p.Err != nil {
			summary += fmt.Sprintf(" — error: %s", p.Err.Error())
		}
		out = append(out, summary)
	}
	return out
}

// List returns the IDs and statuses of all tracked processes — useful when the
// model loses track of an id or wants to enumerate work in flight.
func (r *Registry) List() []Process {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Process, 0, len(r.procs))
	for _, p := range r.procs {
		out = append(out, Process{
			ID:         p.ID,
			Command:    p.Command,
			WorkingDir: p.WorkingDir,
			Status:     p.Status,
			ExitCode:   p.ExitCode,
			Started:    p.Started,
			Finished:   p.Finished,
		})
	}
	return out
}

// writerAdapter routes os/exec stdout/stderr writes into the per-Process
// buffer under the registry mutex so concurrent reads via Status() see a
// consistent snapshot. The buffer is uncapped during write — capTail is
// applied at snapshot time so we don't lose latest output to a head-trim
// race with the model polling status.
type writerAdapter struct {
	p     *Process
	which string
}

func (w *writerAdapter) Write(b []byte) (int, error) {
	if w.which == "stdout" {
		w.p.stdoutBuf.Write(b)
	} else {
		w.p.stderrBuf.Write(b)
	}
	return len(b), nil
}

func capTail(b []byte) []byte {
	if len(b) <= maxBufBytes {
		return append([]byte(nil), b...)
	}
	marker := []byte("...[truncated]...\n")
	keep := maxBufBytes - len(marker)
	out := make([]byte, 0, maxBufBytes)
	out = append(out, marker...)
	out = append(out, b[len(b)-keep:]...)
	return out
}

func newID() (string, error) {
	var buf [4]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return "bg-" + hex.EncodeToString(buf[:]), nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
